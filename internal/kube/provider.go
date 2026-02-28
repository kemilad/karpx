package kube

import (
	"context"
	"strings"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
)

// Provider identifies the infrastructure platform a Kubernetes cluster runs on.
type Provider string

const (
	ProviderAWS     Provider = "aws"
	ProviderAzure   Provider = "azure"
	ProviderGCP     Provider = "gcp"
	ProviderUnknown Provider = "unknown"
)

// ProviderMeta holds display and support information for a provider.
type ProviderMeta struct {
	Label        string // short display name, e.g. "AWS EKS"
	SupportLevel string // "full" | "preview" | "experimental" | "unsupported"
	ChartRepo    string // OCI / Helm chart reference
	DocsURL      string
	ProviderRepo string // upstream GitHub repo URL
}

var providerMeta = map[Provider]ProviderMeta{
	ProviderAWS: {
		Label:        "AWS EKS",
		SupportLevel: "full",
		ChartRepo:    "oci://public.ecr.aws/karpenter/karpenter",
		DocsURL:      "https://karpenter.sh/docs/getting-started/getting-started-with-karpenter/",
		ProviderRepo: "https://github.com/aws/karpenter-provider-aws",
	},
	ProviderAzure: {
		Label:        "Azure AKS",
		SupportLevel: "preview",
		ChartRepo:    "oci://mcr.microsoft.com/aks/karpenter/karpenter",
		DocsURL:      "https://learn.microsoft.com/en-us/azure/aks/karpenter-overview",
		ProviderRepo: "https://github.com/Azure/karpenter-provider-azure-aks",
	},
	ProviderGCP: {
		Label:        "GCP GKE",
		SupportLevel: "experimental",
		ChartRepo:    "oci://us-east1-docker.pkg.dev/k8s-staging-karpenter/karpenter/karpenter",
		DocsURL:      "https://github.com/kubernetes-sigs/karpenter-provider-gcp#readme",
		ProviderRepo: "https://github.com/kubernetes-sigs/karpenter-provider-gcp",
	},
	ProviderUnknown: {
		Label:        "On-prem / Other",
		SupportLevel: "unsupported",
	},
}

// Meta returns display metadata for this provider.
func (p Provider) Meta() ProviderMeta {
	if m, ok := providerMeta[p]; ok {
		return m
	}
	return providerMeta[ProviderUnknown]
}

// Supported returns true when a Karpenter provider exists for this platform.
func (p Provider) Supported() bool { return p != ProviderUnknown }

// ParseProvider converts a user-supplied flag value (e.g. "aws", "gke") to a Provider.
func ParseProvider(s string) Provider {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "aws", "eks":
		return ProviderAWS
	case "azure", "aks":
		return ProviderAzure
	case "gcp", "gke", "google":
		return ProviderGCP
	default:
		return ProviderUnknown
	}
}

// DetectProvider attempts to identify the cloud provider for a cluster by:
//  1. Parsing the kubeconfig server URL (instant, no API call)
//  2. Falling back to reading a node's spec.providerID (one cluster call)
//
// Returns ProviderUnknown when detection fails (e.g. on-prem, local clusters).
func DetectProvider(kubeCtx string) Provider {
	// ── Step 1: kubeconfig server URL ─────────────────────────────────────
	rules := clientcmd.NewDefaultClientConfigLoadingRules()
	cfg, err := rules.Load()
	if err == nil {
		name := cfg.CurrentContext
		if kubeCtx != "" {
			name = kubeCtx
		}
		if kubeCtxObj, ok := cfg.Contexts[name]; ok {
			if cluster, ok := cfg.Clusters[kubeCtxObj.Cluster]; ok {
				if p := fromServerURL(cluster.Server); p != ProviderUnknown {
					return p
				}
			}
		}
	}

	// ── Step 2: node providerID ────────────────────────────────────────────
	return fromNodeProviderID(kubeCtx)
}

// ─────────────────────────────────────────────────────────────────────────────
// Detection helpers
// ─────────────────────────────────────────────────────────────────────────────

func fromServerURL(serverURL string) Provider {
	u := strings.ToLower(serverURL)
	switch {
	case strings.Contains(u, "eks.amazonaws.com"),
		strings.Contains(u, ".elb.amazonaws.com"):
		return ProviderAWS
	case strings.Contains(u, "azmk8s.io"),
		strings.Contains(u, ".azure.com"):
		return ProviderAzure
	case strings.Contains(u, "googleapis.com"),
		strings.Contains(u, ".gke.io"):
		return ProviderGCP
	}
	return ProviderUnknown
}

func fromNodeProviderID(kubeCtx string) Provider {
	overrides := &clientcmd.ConfigOverrides{}
	if kubeCtx != "" {
		overrides.CurrentContext = kubeCtx
	}
	restCfg, err := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(
		clientcmd.NewDefaultClientConfigLoadingRules(), overrides,
	).ClientConfig()
	if err != nil {
		return ProviderUnknown
	}
	cs, err := kubernetes.NewForConfig(restCfg)
	if err != nil {
		return ProviderUnknown
	}
	nodes, err := cs.CoreV1().Nodes().List(context.TODO(), metav1.ListOptions{Limit: 1})
	if err != nil || len(nodes.Items) == 0 {
		return ProviderUnknown
	}
	return fromProviderIDString(nodes.Items[0].Spec.ProviderID)
}

func fromProviderIDString(pid string) Provider {
	p := strings.ToLower(pid)
	switch {
	case strings.HasPrefix(p, "aws://"),
		strings.Contains(p, "amazonaws.com"):
		return ProviderAWS
	case strings.HasPrefix(p, "azure://"),
		strings.Contains(p, "microsoft.compute"):
		return ProviderAzure
	case strings.HasPrefix(p, "gce://"),
		strings.HasPrefix(p, "gcp://"):
		return ProviderGCP
	}
	return ProviderUnknown
}
