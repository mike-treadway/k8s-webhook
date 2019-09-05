package server

import (
	"fmt"
	"strings"

	corev1 "k8s.io/api/core/v1"
)

func createEnvVarFromFieldPath(envVarName, fieldPath string) corev1.EnvVar {
	return corev1.EnvVar{Name: envVarName, ValueFrom: &corev1.EnvVarSource{FieldRef: &corev1.ObjectFieldSelector{FieldPath: fieldPath}}}
}

func createEnvVarFromString(envVarName, envVarValue string) corev1.EnvVar {
	return corev1.EnvVar{Name: envVarName, Value: envVarValue}
}

type metadataEnvGenerator struct {
	clusterName string
}

func (m *metadataEnvGenerator) getVars(pod *corev1.Pod, container *corev1.Container) []corev1.EnvVar {
	vars := []corev1.EnvVar{
		createEnvVarFromString("NEW_RELIC_METADATA_KUBERNETES_CLUSTER_NAME", m.clusterName),
		createEnvVarFromFieldPath("NEW_RELIC_METADATA_KUBERNETES_NODE_NAME", "spec.nodeName"),
		createEnvVarFromFieldPath("NEW_RELIC_METADATA_KUBERNETES_NAMESPACE_NAME", "metadata.namespace"),
		createEnvVarFromFieldPath("NEW_RELIC_METADATA_KUBERNETES_POD_NAME", "metadata.name"),
		createEnvVarFromString("NEW_RELIC_METADATA_KUBERNETES_CONTAINER_NAME", container.Name),
		createEnvVarFromString("NEW_RELIC_METADATA_KUBERNETES_CONTAINER_IMAGE_NAME", container.Image),
		createEnvVarFromFieldPath("NRIA_DISPLAY_NAME", "spec.nodeName"),
	}

	if len(pod.OwnerReferences) == 1 {
		var ownerName, envName string
		switch pod.OwnerReferences[0].Kind {
		case "ReplicaSet":
			// Guess the name of the deployment. We check whether the Pod is Owned by a ReplicaSet and confirms with the
			// naming convention for a Deployment. This can give a false positive if the user uses ReplicaSets directly.
			podParts := strings.Split(pod.GenerateName, "-")
			if len(podParts) >= 3 {
				ownerName = strings.Join(podParts[:len(podParts)-2], "-")
				envName = "NEW_RELIC_METADATA_KUBERNETES_DEPLOYMENT_NAME"
			}
		case "StatefulSet":
			ownerName = pod.OwnerReferences[0].Name
			envName = "NEW_RELIC_METADATA_KUBERNETES_STATEFULSET_NAME"
		case "DaemonSet":
			ownerName = pod.OwnerReferences[0].Name
			envName = "NEW_RELIC_METADATA_KUBERNETES_DAEMONSET_NAME"
		}
		if len(envName) > 0 {
			vars = append(vars, createEnvVarFromString(envName, ownerName))
		}
	}

	return vars
}

// EnvVarMutator - injects NewRelic metadata env vars into pods
type EnvVarMutator struct {
	envGenerator *metadataEnvGenerator
}

// NewEnvVarMutator - return new env var pod mutator
func NewEnvVarMutator(clusterName string) *EnvVarMutator {
	return &EnvVarMutator{
		envGenerator: &metadataEnvGenerator{
			clusterName: clusterName,
		},
	}
}

func (evm *EnvVarMutator) updateContainer(pod *corev1.Pod, index int, container *corev1.Container) (patch []PatchOperation) {
	// Create map with all environment variable names
	envVarMap := map[string]bool{}
	for _, envVar := range container.Env {
		envVarMap[envVar.Name] = true
	}

	// Create a patch for each EnvVar in toInject (if they are not yet defined on the container)
	first := len(envVarMap) == 0
	var value interface{}
	basePath := fmt.Sprintf("/spec/containers/%d/env", index)

	for _, inject := range evm.envGenerator.getVars(pod, container) {
		if _, present := envVarMap[inject.Name]; !present {
			value = inject
			path := basePath

			if first {
				// For the first element we have to create the list
				value = []corev1.EnvVar{inject}
				first = false
			} else {
				// For the other elements we can append to the list
				path = path + "/-"
			}

			patch = append(patch, PatchOperation{
				Op:    "add",
				Path:  path,
				Value: value,
			})
		}
	}
	return patch
}

// Mutate - update the env vars for each container in pod
func (evm *EnvVarMutator) Mutate(pod *corev1.Pod) ([]PatchOperation, error) {
	var patch []PatchOperation

	for i, container := range pod.Spec.Containers {
		patch = append(patch, evm.updateContainer(pod, i, &container)...)
	}

	return patch, nil
}
