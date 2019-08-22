package server

import (
	"fmt"
	"net/http"
	"os"
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
	tmpfsDataVolumeName            = "tmpfs-data"
	tmpfsUserDataVolumeName        = "tmpfs-user-data"
	tmpfsTmpVolumeName             = "tmpfs-tmp"
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

// SidecarMutator - injects sidecars into pods
type SidecarMutator struct {
	clusterName         string
	containerDefinition *corev1.Container
	envGenerator        *metadataEnvGenerator
	cfgMapRtrv          configMapRetriever
	nriaEnvVars         map[string]string
}

type configMapRetriever interface {
	ConfigMap(namespace, name string) (*corev1.ConfigMap, error)
}

func boolPointer(b bool) *bool {
	return &b
}

func int64Pointer(i int64) *int64 {
	return &i
}

// NewSidecarMutator - create new sidecar mutator instance
func NewSidecarMutator(clusterName string, cfgMapRtrv configMapRetriever) *SidecarMutator {
	sm := &SidecarMutator{
		clusterName: clusterName,
		containerDefinition: &corev1.Container{
			Name:            "newrelic-sidecar",
			ImagePullPolicy: corev1.PullIfNotPresent,
			Image:           defaultIntegrationImage,
			SecurityContext: &corev1.SecurityContext{
				AllowPrivilegeEscalation: boolPointer(false),
				Privileged:               boolPointer(false),
				RunAsNonRoot:             boolPointer(true),
				ReadOnlyRootFilesystem:   boolPointer(false),
				RunAsUser:                int64Pointer(1000),
			},
		},
		envGenerator: &metadataEnvGenerator{
			clusterName: clusterName,
		},
		cfgMapRtrv:  cfgMapRtrv,
		nriaEnvVars: map[string]string{},
	}
	// pass all env vars starting with NRIA in the injector to the sidecar (line the license)
	for _, e := range os.Environ() {
		if strings.HasPrefix(e, "NRIA") {
			splits := strings.SplitN(e, "=", 2)
			if len(splits) == 2 && splits[1] != "" {
				sm.nriaEnvVars[splits[0]] = splits[1]
			}
		}
	}
	return sm
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
func (sm *SidecarMutator) mutationRequired(pod *corev1.Pod) bool {
	annotations := pod.GetAnnotations()

	return strings.ToLower(annotations[annotationStatusKey]) != injected &&
		annotations[annotationIntegrationConfigKey] != ""
}

func addContainer(target, added []corev1.Container, basePath string) (patch []PatchOperation) {
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
		patch = append(patch, PatchOperation{
			Op:    "add",
			Path:  path,
			Value: value,
		})
	}
	return patch
}

func addVolume(target, added []corev1.Volume, basePath string) (patch []PatchOperation) {
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
		patch = append(patch, PatchOperation{
			Op:    "add",
			Path:  path,
			Value: value,
		})
	}
	return patch
}

func updateAnnotation(target map[string]string, added map[string]string) (patch []PatchOperation) {
	for key, value := range added {
		if target == nil || target[key] == "" {
			target = map[string]string{}
			patch = append(patch, PatchOperation{
				Op:   "add",
				Path: "/metadata/annotations",
				Value: map[string]string{
					key: value,
				},
			})
		} else {
			patch = append(patch, PatchOperation{
				Op:    "replace",
				Path:  "/metadata/annotations/" + key,
				Value: value,
			})
		}
	}
	return patch
}

// Mutate - inject the sidecar into the pod
func (sm *SidecarMutator) Mutate(pod *corev1.Pod) ([]PatchOperation, error) {
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
func (sm *SidecarMutator) createPatch(pod *corev1.Pod, containers []corev1.Container, volumes []corev1.Volume, annotations map[string]string) ([]PatchOperation, error) {
	var patch []PatchOperation

	patch = append(patch, addContainer(pod.Spec.Containers, containers, "/spec/containers")...)
	patch = append(patch, addVolume(pod.Spec.Volumes, volumes, "/spec/volumes")...)
	patch = append(patch, updateAnnotation(pod.Annotations, annotations)...)

	return patch, nil
}

func (sm *SidecarMutator) addEnvVars(pod *corev1.Pod, sidecar *corev1.Container, envToArgs map[string]string) {
	sidecar.Env = sm.envGenerator.getVars(pod, &pod.Spec.Containers[0])

	sidecar.Env = append(sidecar.Env, []corev1.EnvVar{
		createEnvVarFromString("NRIA_IS_FORWARD_ONLY", "true"),
		createEnvVarFromString("NRIA_OVERRIDE_HOST_ROOT", ""),
		createEnvVarFromString("K8S_INTEGRATION", "true"),
	}...)

	for k, v := range sm.nriaEnvVars {
		sidecar.Env = append(sidecar.Env, corev1.EnvVar{
			Name:  k,
			Value: v,
		})
	}

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

	if len(pod.Spec.Containers) > 0 {
		for _, env := range pod.Spec.Containers[0].Env {
			if arg := envToArgs[env.Name]; arg != "" {
				e := env.DeepCopy()
				e.Name = arg
				sidecar.Env = append(sidecar.Env, *e)
			}
		}
	}
	if len(envToArgs) > 0 {
		envs := []string{}
		for _, v := range envToArgs {
			envs = append(envs, v)
		}
		sidecar.Env = append(sidecar.Env, createEnvVarFromString("NRIA_PASSTHROUGH_ENVIRONMENT", strings.Join(envs, ",")))
	}

}

type integrationCfg struct {
	Instances []struct {
		Arguments map[string]string `yaml:"arguments"`
	} `yaml:"instances"`
}

func (sm *SidecarMutator) createSidecar(pod *corev1.Pod) ([]corev1.Container, []corev1.Volume, error) {
	containerDef := *sm.containerDefinition
	annotations := pod.GetAnnotations()

	if configImageName := annotations[annotationIntegrationImage]; configImageName != "" {
		containerDef.Image = configImageName
	}
	configMapName := annotations[annotationIntegrationConfigKey]

	cfgMap, err := sm.cfgMapRtrv.ConfigMap(pod.Namespace, configMapName)
	if err != nil {
		if k8s_errors.IsNotFound(err) {
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

	envToArgs := map[string]string{}
	for _, inst := range intCfg.Instances {
		for k, v := range inst.Arguments {
			if strings.HasPrefix(v, "$") {
				envToArgs[v[1:]] = strings.ToUpper(k)
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
		{
			Name: tmpfsDataVolumeName,
		},
		{
			Name: tmpfsUserDataVolumeName,
		},
		{
			Name: tmpfsTmpVolumeName,
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
		{
			Name:      tmpfsDataVolumeName,
			MountPath: "/var/db/newrelic-infra/data",
		},
		{
			Name:      tmpfsUserDataVolumeName,
			MountPath: "/var/db/newrelic-infra/user_data",
		},
		{
			Name:      tmpfsTmpVolumeName,
			MountPath: "/tmp",
		},
	}

	// map the rest of the ConfigMap
	if len(cfgMap.Data) > 2 {
		for k := range cfgMap.Data {
			if k != configKey && k != definitionKey {
				vol := corev1.VolumeMount{
					Name:      integrationConfigVolumeName,
					MountPath: "/var/db/newrelic-infra/user_data/" + k,
					SubPath:   k,
				}
				containerDef.VolumeMounts = append(containerDef.VolumeMounts, vol)
			}
		}
	}

	if len(pod.Spec.Containers) > 0 {
		for _, vol := range pod.Spec.Containers[0].VolumeMounts {
			volCp := vol.DeepCopy()
			volCp.ReadOnly = true
			containerDef.VolumeMounts = append(containerDef.VolumeMounts, *volCp)
		}
	}

	sm.addEnvVars(pod, &containerDef, envToArgs)

	return []corev1.Container{containerDef}, volumes, nil
}
