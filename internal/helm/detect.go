// Package helm provides Karpenter detection via the Helm CLI.
// It shells out to `helm list` so no Helm library dependency is required,
// and it uses whatever cluster credentials kubectl already has configured.
package helm

import (
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
)

// Info describes the Karpenter installation found (or not) on a cluster.
type Info struct {
	Installed   bool
	ReleaseName string // helm release name, e.g. "karpenter"
	Version     string // Karpenter app version, e.g. "1.2.1"
	Namespace   string
	Chart       string
}

type helmRelease struct {
	Name       string `json:"name"`
	Namespace  string `json:"namespace"`
	Chart      string `json:"chart"`
	AppVersion string `json:"app_version"`
	Status     string `json:"status"`
}

// DetectKarpenter lists all Helm releases in every namespace for the given
// kubeconfig context and returns Info for the first release whose name or
// chart name contains "karpenter".
//
// If helm is not on PATH or no Karpenter release is found, returns
// Info{Installed: false} with no error.
func DetectKarpenter(kubeCtx string) (*Info, error) {
	args := []string{"list", "--all-namespaces", "--output", "json"}
	if kubeCtx != "" {
		args = append(args, "--kube-context", kubeCtx)
	}

	out, err := exec.Command("helm", args...).Output()
	if err != nil {
		// helm not installed or cluster unreachable — treat as not installed.
		return &Info{Installed: false}, nil
	}

	var releases []helmRelease
	if err := json.Unmarshal(out, &releases); err != nil {
		return &Info{Installed: false}, nil
	}

	for _, r := range releases {
		if isKarpenterRelease(r) {
			return &Info{
				Installed:   true,
				ReleaseName: r.Name,
				Version:     strings.TrimPrefix(r.AppVersion, "v"),
				Namespace:   r.Namespace,
				Chart:       r.Chart,
			}, nil
		}
	}
	return &Info{Installed: false}, nil
}

// isKarpenterRelease returns true when the Helm release name or chart name
// looks like Karpenter (covers both upstream and Karpenter provider variants).
func isKarpenterRelease(r helmRelease) bool {
	name := strings.ToLower(r.Name)
	chart := strings.ToLower(r.Chart)
	return strings.Contains(name, "karpenter") || strings.Contains(chart, "karpenter")
}

// EnsureHelmAvailable returns an error when helm is not on PATH.
func EnsureHelmAvailable() error {
	if _, err := exec.LookPath("helm"); err != nil {
		return fmt.Errorf("helm not found on PATH — install helm ≥ 3: https://helm.sh/docs/intro/install/")
	}
	return nil
}
