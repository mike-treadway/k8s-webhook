package main

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/pkg/errors"
	yaml "gopkg.in/yaml.v2"

	corev1 "k8s.io/api/core/v1"
	k8s_errors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
)

const (
	annotationIntegrationConfigKey = "newrelic.com/integrations-sidecar-configmap"
	annotationIntegrationImage     = "newrelic.com/integrations-sidecar-imagename"
	annotationStatusKey            = "newrelic.com/integrations-sidecar-injector-status"
	integrationConfigVolumeName    = "integration-config"
	defaultIntegrationImage        = "sidecar-image"
	configKey                      = "config.yaml"
	definitionKey                  = "definition.yaml"
	injected                       = "injected"
	maxLabelsCount                 = 50
)

var (
	// (https://github.com/kubernetes/kubernetes/issues/57982)
	defaulter = runtime.ObjectDefaulter(runtimeScheme)
)

type sidecarMutator struct {
	clusterName         string
	containerDefinition *corev1.Container
	envGenerator        *metadataEnvGenerator
	cfgMapRtrv          configMapRetriever
}

type configMapRetriever interface {
	ConfigMap(namespace, name string) (*corev1.ConfigMap, error)
}

func newSidecarMutator(clusterName string, cfgMapRtrv configMapRetriever) *sidecarMutator {
	return &sidecarMutator{
		clusterName: clusterName,
		containerDefinition: &corev1.Container{
			Name:            "newrelic-sidecar",
			ImagePullPolicy: corev1.PullIfNotPresent,
			Image:           defaultIntegrationImage,
		},
		envGenerator: &metadataEnvGenerator{
			clusterName: clusterName,
		},
		cfgMapRtrv: cfgMapRtrv,
	}
}

type mutateError struct {
	message string
	code    int
}

func (me mutateError) Error() string {
	return me.message
}

func (me mutateError) Code() int {
	return me.code
}

// (https://github.com/kubernetes/kubernetes/issues/57982)
func applyDefaultsWorkaround(containers []corev1.Container, volumes []corev1.Volume) {
	defaulter.Default(&corev1.Pod{
		Spec: corev1.PodSpec{
			Containers: containers,
			Volumes:    volumes,
		},
	})
}

// Check whether the target resoured need to be mutated
func (sm *sidecarMutator) mutationRequired(pod *corev1.Pod) bool {
	annotations := pod.GetAnnotations()

	// determine whether to perform mutation based on annotation for the target resource
	var required bool
	if strings.ToLower(annotations[annotationStatusKey]) == injected {
		required = false
	} else {
		required = annotations[annotationIntegrationConfigKey] != ""
	}

	return required
}

func addContainer(target, added []corev1.Container, basePath string) (patch []patchOperation) {
	first := len(target) == 0
	var value interface{}
	for _, add := range added {
		value = add
		path := basePath
		if first {
			first = false
			value = []corev1.Container{add}
		} else {
			path = path + "/-"
		}
		patch = append(patch, patchOperation{
			Op:    "add",
			Path:  path,
			Value: value,
		})
	}
	return patch
}

func addVolume(target, added []corev1.Volume, basePath string) (patch []patchOperation) {
	first := len(target) == 0
	var value interface{}
	for _, add := range added {
		value = add
		path := basePath
		if first {
			first = false
			value = []corev1.Volume{add}
		} else {
			path = path + "/-"
		}
		patch = append(patch, patchOperation{
			Op:    "add",
			Path:  path,
			Value: value,
		})
	}
	return patch
}

func updateAnnotation(target map[string]string, added map[string]string) (patch []patchOperation) {
	for key, value := range added {
		if target == nil || target[key] == "" {
			target = map[string]string{}
			patch = append(patch, patchOperation{
				Op:   "add",
				Path: "/metadata/annotations",
				Value: map[string]string{
					key: value,
				},
			})
		} else {
			patch = append(patch, patchOperation{
				Op:    "replace",
				Path:  "/metadata/annotations/" + key,
				Value: value,
			})
		}
	}
	return patch
}

func (sm *sidecarMutator) mutate(pod *corev1.Pod) ([]patchOperation, error) {
	// determine whether to perform mutation
	if !sm.mutationRequired(pod) {
		return nil, nil
	}

	containers, volumes, err := sm.createSidecar(pod)
	if err != nil {
		return nil, err
	}

	// Workaround: https://github.com/kubernetes/kubernetes/issues/57982
	applyDefaultsWorkaround(containers, volumes)

	return sm.createPatch(pod, containers, volumes, map[string]string{annotationStatusKey: injected})
}

// create mutation patch for resoures
func (sm *sidecarMutator) createPatch(pod *corev1.Pod, containers []corev1.Container, volumes []corev1.Volume, annotations map[string]string) ([]patchOperation, error) {
	var patch []patchOperation

	patch = append(patch, addContainer(pod.Spec.Containers, containers, "/spec/containers")...)
	patch = append(patch, addVolume(pod.Spec.Volumes, volumes, "/spec/volumes")...)
	patch = append(patch, updateAnnotation(pod.Annotations, annotations)...)

	return patch, nil
}

func (sm *sidecarMutator) addEnvVars(pod *corev1.Pod, sidecar *corev1.Container) {
	sidecar.Env = sm.envGenerator.getVars(pod, &pod.Spec.Containers[0])

	sidecar.Env = append(sidecar.Env, []corev1.EnvVar{
		createEnvVarFromString("NRIA_IS_FORWARD_ONLY", "true"),
		createEnvVarFromString("NRIA_OVERRIDE_HOST_ROOT", ""),
	}...)

	labels := ""
	i := 0
	for k, v := range pod.Labels {
		labels += fmt.Sprintf("%s=%s,", k, v)
		i++
		// limit number of labels
		if i > maxLabelsCount {
			break
		}
	}
	if len(labels) > 0 {
		sidecar.Env = append(sidecar.Env, createEnvVarFromString("NEW_RELIC_METADATA_KUBERNETES_LABELS", labels[:len(labels)-1]))
	}
}

type integrationCfg struct {
	Instances []struct {
		Arguments map[string]string `yaml:"arguments"`
	} `yaml:"instances"`
}

func (sm *sidecarMutator) createSidecar(pod *corev1.Pod) ([]corev1.Container, []corev1.Volume, error) {
	containerDef := *sm.containerDefinition
	annotations := pod.GetAnnotations()

	if configImageName := annotations[annotationIntegrationImage]; configImageName != "" {
		containerDef.Image = configImageName
	}
	configMapName := annotations[annotationIntegrationConfigKey]

	cfgMap, err := sm.cfgMapRtrv.ConfigMap(pod.Namespace, configMapName)
	if err != nil {
		if k8s_errors.IsNotFound(err) {
			fmt.Printf("\nNOT FOUND\n")
			return nil, nil, &mutateError{
				message: fmt.Sprintf("config map: '%s', not found", configMapName),
				code:    http.StatusBadRequest,
			}
		}
		return nil, nil, errors.Wrapf(err, "error retrieving config map '%s'", configMapName)
	}

	var intCfg integrationCfg
	err = yaml.Unmarshal([]byte(cfgMap.Data[configKey]), &intCfg)
	if err != nil {
		return nil, nil, errors.Wrapf(err, "error unmarshaling integration config: %s", configMapName)
	}

	envArgs := map[string]string{}
	for _, inst := range intCfg.Instances {
		for k, v := range inst.Arguments {
			if strings.HasPrefix(v, "$") {
				envArgs[v[1:]] = strings.ToUpper(k)
			}
		}
	}

	volumes := []corev1.Volume{
		{
			Name: integrationConfigVolumeName,
			VolumeSource: corev1.VolumeSource{
				ConfigMap: &corev1.ConfigMapVolumeSource{
					LocalObjectReference: corev1.LocalObjectReference{
						Name: configMapName,
					},
				},
			},
		},
	}
	containerDef.VolumeMounts = []corev1.VolumeMount{
		{
			Name:      integrationConfigVolumeName,
			MountPath: "/var/db/newrelic-infra/integrations.d/integration.yaml",
			SubPath:   configKey,
		},
		{
			Name:      integrationConfigVolumeName,
			MountPath: "/var/db/newrelic-infra/newrelic-integrations/definition.yaml",
			SubPath:   definitionKey,
		},
	}
	if len(pod.Spec.Containers) > 0 {
		for _, vol := range pod.Spec.Containers[0].VolumeMounts {
			volCp := vol.DeepCopy()
			volCp.ReadOnly = true
			containerDef.VolumeMounts = append(containerDef.VolumeMounts, *volCp)
		}
		for _, env := range pod.Spec.Containers[0].Env {
			if envArgs[env.Name] != "" {
				e := env.DeepCopy()
				e.Name = envArgs[env.Name]
				containerDef.Env = append(containerDef.Env, *e)
			}
		}
	}

	sm.addEnvVars(pod, &containerDef)
	if len(envArgs) > 0 {
		envs := []string{}
		for _, v := range envArgs {
			envs = append(envs, v)
		}
		containerDef.Env = append(containerDef.Env, createEnvVarFromString("NRIA_PASSTHROUGH_ENVIRONMENT", strings.Join(envs, ",")))
	}

	return []corev1.Container{containerDef}, volumes, nil
}
