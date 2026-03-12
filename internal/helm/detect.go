// Package helm provides Karpenter detection via the Helm CLI.
// It shells out to `helm list` so no Helm library dependency is required,
// and it uses whatever cluster credentials kubectl already has configured.
package helm

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
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
		// helm is not available or the invocation failed — fall back to the
		// Kubernetes API so we still detect Karpenter installed via manifests
		// or when helm is broken/absent.
		return detectViaKubeAPI(kubeCtx)
	}

	var releases []helmRelease
	if err := json.Unmarshal(out, &releases); err != nil {
		// Malformed output from helm — try API fallback before giving up.
		return detectViaKubeAPI(kubeCtx)
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

	// Helm didn't find Karpenter — fall back to Kubernetes API detection.
	// This covers clusters where Karpenter was installed outside of Helm
	// (raw manifests, older tooling, operators, etc.).
	return detectViaKubeAPI(kubeCtx)
}

// isKarpenterRelease returns true when the Helm release name or chart name
// looks like Karpenter (covers both upstream and Karpenter provider variants).
func isKarpenterRelease(r helmRelease) bool {
	name := strings.ToLower(r.Name)
	chart := strings.ToLower(r.Chart)
	return strings.Contains(name, "karpenter") || strings.Contains(chart, "karpenter")
}

// detectViaKubeAPI is a fallback for clusters where Karpenter was not installed
// through Helm. It checks the cluster's API server for the karpenter.sh API
// group (which confirms the CRDs are registered) and then looks for a
// Deployment labelled app.kubernetes.io/name=karpenter to determine the
// version from the controller image tag.
func detectViaKubeAPI(kubeCtx string) (*Info, error) {
	overrides := &clientcmd.ConfigOverrides{}
	if kubeCtx != "" {
		overrides.CurrentContext = kubeCtx
	}
	restCfg, err := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(
		clientcmd.NewDefaultClientConfigLoadingRules(), overrides,
	).ClientConfig()
	if err != nil {
		return &Info{Installed: false}, nil
	}
	cs, err := kubernetes.NewForConfig(restCfg)
	if err != nil {
		return &Info{Installed: false}, nil
	}

	// Confirm Karpenter CRDs are registered by checking API groups.
	groups, err := cs.Discovery().ServerGroups()
	if err != nil {
		return &Info{Installed: false}, nil
	}
	hasCRDs := false
	for _, g := range groups.Groups {
		if strings.Contains(g.Name, "karpenter") {
			hasCRDs = true
			break
		}
	}
	if !hasCRDs {
		return &Info{Installed: false}, nil
	}

	// CRDs exist — now find the controller Deployment to get the version.
	deps, err := cs.AppsV1().Deployments("").List(context.TODO(), metav1.ListOptions{
		LabelSelector: "app.kubernetes.io/name=karpenter",
	})
	// Older Karpenter releases (pre-v0.20) used "app=karpenter" instead.
	if err != nil || len(deps.Items) == 0 {
		deps, _ = cs.AppsV1().Deployments("").List(context.TODO(), metav1.ListOptions{
			LabelSelector: "app=karpenter",
		})
	}
	if len(deps.Items) == 0 {
		// CRDs present but no standard deployment found; still installed.
		return &Info{Installed: true}, nil
	}

	dep := deps.Items[0]
	version := ""
	for _, c := range dep.Spec.Template.Spec.Containers {
		if v := imageTagVersion(c.Image); v != "" {
			version = v
			break
		}
	}
	// Try init containers too (some Karpenter builds use them for version).
	if version == "" {
		for _, c := range dep.Spec.Template.Spec.InitContainers {
			if v := imageTagVersion(c.Image); v != "" {
				version = v
				break
			}
		}
	}

	return &Info{
		Installed:   true,
		ReleaseName: dep.Name,
		Version:     version,
		Namespace:   dep.Namespace,
	}, nil
}

// imageTagVersion extracts a semver-like version string from a container image
// reference. E.g. "public.ecr.aws/karpenter/controller:v1.2.1" → "1.2.1".
// Returns empty string for non-version tags such as git SHAs or "latest".
func imageTagVersion(image string) string {
	idx := strings.LastIndex(image, ":")
	if idx < 0 {
		return ""
	}
	tag := strings.TrimPrefix(image[idx+1:], "v")
	// Only accept semver-like tags that start with a digit (e.g. "1.2.3").
	// Reject git SHAs, "latest", and other non-version strings.
	if tag == "" || tag[0] < '0' || tag[0] > '9' {
		return ""
	}
	return tag
}

// EnsureHelmAvailable returns an error when helm is not on PATH.
func EnsureHelmAvailable() error {
	if _, err := exec.LookPath("helm"); err != nil {
		return fmt.Errorf("helm not found on PATH — install helm ≥ 3: https://helm.sh/docs/intro/install/")
	}
	return nil
}
