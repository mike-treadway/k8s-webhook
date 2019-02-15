package k8s

import (
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

// Client wraps a connection to K8s api
type Client struct {
	clientset *kubernetes.Clientset
}

// New create new kubernetes client
func New() (*Client, error) {
	// Create the in-cluster config
	config, err := rest.InClusterConfig()
	if err != nil {
		return nil, err
	}

	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		return nil, err
	}

	return &Client{
		clientset: clientset,
	}, nil
}

// ConfigMap - retrieve a config map from the K8s api
func (kc *Client) ConfigMap(namespace, name string) (*corev1.ConfigMap, error) {
	return kc.clientset.CoreV1().ConfigMaps(namespace).Get(name, metav1.GetOptions{})
}
