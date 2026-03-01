package kube

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
)

// NamespaceStatus describes the result of EnsureNamespace.
type NamespaceStatus int

const (
	NamespaceExisted NamespaceStatus = iota
	NamespaceCreated
)

// EnsureNamespace checks whether the given namespace exists and creates it if
// it does not. Returns the status (existed/created) and any error.
func EnsureNamespace(kubeCtx, namespace string) (NamespaceStatus, error) {
	overrides := &clientcmd.ConfigOverrides{}
	if kubeCtx != "" {
		overrides.CurrentContext = kubeCtx
	}
	restCfg, err := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(
		clientcmd.NewDefaultClientConfigLoadingRules(), overrides,
	).ClientConfig()
	if err != nil {
		return 0, fmt.Errorf("load kubeconfig: %w", err)
	}
	cs, err := kubernetes.NewForConfig(restCfg)
	if err != nil {
		return 0, err
	}

	_, err = cs.CoreV1().Namespaces().Get(context.TODO(), namespace, metav1.GetOptions{})
	if err == nil {
		return NamespaceExisted, nil
	}
	if !k8serrors.IsNotFound(err) {
		return 0, fmt.Errorf("check namespace %q: %w", namespace, err)
	}

	ns := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name: namespace,
			Labels: map[string]string{
				"app.kubernetes.io/managed-by": "karpx",
			},
		},
	}
	if _, err := cs.CoreV1().Namespaces().Create(context.TODO(), ns, metav1.CreateOptions{}); err != nil {
		return 0, fmt.Errorf("create namespace %q: %w", namespace, err)
	}
	return NamespaceCreated, nil
}
