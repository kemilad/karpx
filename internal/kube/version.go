// Package kube provides helpers for interacting with a Kubernetes cluster
// using the official client-go library.
package kube

import (
	"fmt"
	"strings"

	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
)

// GetServerVersion returns the Kubernetes server version for the given
// kubeconfig context as a semver string (e.g. "1.30.2").
// If kubeCtx is empty the current context is used.
func GetServerVersion(kubeCtx string) (string, error) {
	loadingRules := clientcmd.NewDefaultClientConfigLoadingRules()
	overrides := &clientcmd.ConfigOverrides{}
	if kubeCtx != "" {
		overrides.CurrentContext = kubeCtx
	}

	cfg := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(loadingRules, overrides)
	restCfg, err := cfg.ClientConfig()
	if err != nil {
		return "", fmt.Errorf("load kubeconfig: %w", err)
	}

	cs, err := kubernetes.NewForConfig(restCfg)
	if err != nil {
		return "", fmt.Errorf("create kubernetes client: %w", err)
	}

	sv, err := cs.Discovery().ServerVersion()
	if err != nil {
		return "", fmt.Errorf("get server version: %w", err)
	}

	// sv.GitVersion is typically "v1.30.2-eks-…" or "v1.30.2".
	// Strip the "v" prefix and any build metadata after the patch number.
	v := strings.TrimPrefix(sv.GitVersion, "v")
	if idx := strings.IndexAny(v, "-+"); idx != -1 {
		v = v[:idx]
	}
	// Ensure major.minor.patch
	parts := strings.Split(v, ".")
	for len(parts) < 3 {
		parts = append(parts, "0")
	}
	return strings.Join(parts[:3], "."), nil
}
