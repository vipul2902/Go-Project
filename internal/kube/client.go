package kube

import (
	"errors"
	"fmt"

	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

// NewClientset connects to Kubernetes. Inside a cluster it uses the pod's
// service account. Outside, it uses the kubeconfig (KUBECONFIG or
// ~/.kube/config) and requires an explicit context name: a worker must
// never deploy to whatever cluster the developer's shell last pointed at.
func NewClientset(kubeContext string) (*kubernetes.Clientset, error) {
	cfg, err := rest.InClusterConfig()
	if errors.Is(err, rest.ErrNotInCluster) {
		if kubeContext == "" {
			return nil, errors.New("KUBE_CONTEXT is required outside a cluster")
		}
		cfg, err = clientcmd.NewNonInteractiveDeferredLoadingClientConfig(
			clientcmd.NewDefaultClientConfigLoadingRules(),
			&clientcmd.ConfigOverrides{CurrentContext: kubeContext},
		).ClientConfig()
	}
	if err != nil {
		return nil, fmt.Errorf("kubernetes config: %w", err)
	}
	cfg.UserAgent = "clouddeploy-worker"
	return kubernetes.NewForConfig(cfg)
}
