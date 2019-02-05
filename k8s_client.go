package main

import (
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

type k8sClient struct {
	clientset *kubernetes.Clientset
}

func newK8sClient() (*k8sClient, error) {
	// Create the in-cluster config
	config, err := rest.InClusterConfig()
	if err != nil {
		return nil, err
	}

	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		return nil, err
	}

	return &k8sClient{
		clientset: clientset,
	}, nil
}

func (kc *k8sClient) ConfigMap(namespace, name string) (*corev1.ConfigMap, error) {
	return kc.clientset.CoreV1().ConfigMaps(namespace).Get(name, metav1.GetOptions{})
}
