# karpx ⚡

> Karpenter — managed from your terminal

```
  ██╗  ██╗ █████╗ ██████╗ ██████╗ ██╗  ██╗
  ██║ ██╔╝██╔══██╗██╔══██╗██╔══██╗╚██╗██╔╝
  █████╔╝ ███████║██████╔╝██████╔╝ ╚███╔╝
  ██╔═██╗ ██╔══██║██╔══██╗██╔═══╝  ██╔██╗
  ██║  ██╗██║  ██║██║  ██║██║     ██╔╝ ██╗
  ╚═╝  ╚═╝╚═╝  ╚═╝╚═╝  ╚═╝╚═╝     ╚═╝  ╚═╝
```

A single-binary TUI to install, upgrade, and configure [Karpenter](https://karpenter.sh)
across your Kubernetes clusters — no YAML, no context-switching, just your terminal.

## Cloud Provider Support

karpx auto-detects your cluster's cloud provider and guides you through the
correct setup for each platform.

| Provider | Support level | Karpenter provider |
|---|---|---|
| **AWS EKS** | ● Full | [aws/karpenter-provider-aws](https://github.com/aws/karpenter-provider-aws) |
| **Azure AKS** | ◐ Preview | [Azure/karpenter-provider-azure-aks](https://github.com/Azure/karpenter-provider-azure-aks) |
| **GCP GKE** | ◌ Experimental | [kubernetes-sigs/karpenter-provider-gcp](https://github.com/kubernetes-sigs/karpenter-provider-gcp) |
| **On-prem / other** | ✗ Not supported | — Karpenter requires cloud provider APIs |

> **On-prem note:** Karpenter cannot run on bare-metal or on-prem clusters because
> it depends on cloud provider APIs (EC2, Azure VMSS, GCE) to provision nodes.
> Consider [Cluster Autoscaler](https://github.com/kubernetes/autoscaler) or
> [KEDA](https://keda.sh) instead.

## Install karpx

### Homebrew (macOS / Linux) — recommended

```bash
brew tap kemilad/tap
brew install karpx
```

### curl installer

```bash
curl -fsSL https://raw.githubusercontent.com/kemilad/karpx/main/install.sh | bash
```

Override version or install directory:

```bash
VERSION=v0.2.0 INSTALL_DIR=~/.local/bin \
  curl -fsSL https://raw.githubusercontent.com/kemilad/karpx/main/install.sh | bash
```

### go install

```bash
go install github.com/kemilad/karpx@latest
```

### Manual

Download the binary for your platform from [Releases](https://github.com/kemilad/karpx/releases),
extract it, and place it on your `$PATH`.

## Usage

### Interactive TUI

```bash
karpx                            # uses your current kubeconfig context
karpx -c my-eks-prod             # target a specific cluster
karpx -c my-eks-prod -r ap-southeast-1
```

### TUI keyboard shortcuts

| Key | Action |
|-----|--------|
| `↑` `↓` / `j` `k` | Move between clusters |
| `i` | Install Karpenter on selected cluster |
| `u` | Upgrade Karpenter on selected cluster |
| `n` | Manage NodePools / EC2NodeClasses |
| `r` | Refresh cluster list |
| `Esc` | Go back |
| `q` | Quit |

### Node type optimisation

`karpx nodes` analyses your running workloads and asks one question:

```
  What is your node provisioning priority?

    [1]  Cost-Optimized   — Spot + Graviton (arm64), saves 60-80% vs on-demand
    [2]  Balanced         — Mixed Spot + On-Demand, multiple instance families
    [3]  High-Performance — On-Demand only, latest-gen, no interruptions
```

It then generates and optionally applies a Karpenter **NodePool + NodeClass** manifest
tuned to your actual workload profile (CPU/memory ratio, GPU usage, batch jobs).

```bash
karpx nodes -c my-cluster              # interactive: analyse + ask + apply/save
karpx nodes -c my-cluster --mode cost  # skip the question, use cost-optimised
```

### Non-interactive (CI / scripting)

```bash
# Detect cloud provider, Karpenter version, and compatibility.
karpx detect -c my-cluster

# Install — auto-detects provider and asks questions interactively.
karpx install -c my-cluster

# Install non-interactively on AWS EKS.
karpx install --provider aws \
  -c my-cluster \
  --cluster-name my-cluster \
  -r ap-southeast-1 \
  --role-arn arn:aws:iam::123456789012:role/KarpenterController

# Install on Azure AKS (shows guided setup).
karpx install --provider azure -c my-aks-cluster

# Install on GCP GKE (shows guided setup).
karpx install --provider gcp -c my-gke-cluster

# Upgrade to the latest compatible version.
karpx upgrade -c my-cluster

# Upgrade to a specific version.
karpx upgrade -c my-cluster --version v1.3.0

# Analyse workloads and generate an optimised NodePool manifest.
karpx nodes -c my-cluster
karpx nodes -c my-cluster --mode cost        # cost-optimised (Spot + Graviton)
karpx nodes -c my-cluster --mode performance # high-performance (on-demand)

# List NodePools.
karpx nodepools -c my-cluster
karpx np -c my-cluster          # short alias

# Print karpx version.
karpx version
```

### Provider detection

karpx detects your cloud provider automatically by inspecting:

1. The kubeconfig server URL (e.g. `*.eks.amazonaws.com`, `*.azmk8s.io`, `*.googleapis.com`)
2. Node `spec.providerID` as a fallback (requires cluster access)

If detection fails (e.g. private endpoints, custom DNS), pass `--provider` explicitly:

```bash
karpx install --provider aws   -c <context>
karpx install --provider azure -c <context>
karpx install --provider gcp   -c <context>
```

## Requirements

- `kubectl` configured (`~/.kube/config`) with your cluster contexts
- `helm` ≥ 3 on your `$PATH`
- Cloud credentials appropriate for your provider:
  - **AWS** — environment variables, `~/.aws/credentials`, or IAM instance role
  - **Azure** — `az login` or a service principal
  - **GCP** — `gcloud auth application-default login`

## How it works

karpx is a single static binary with zero runtime dependencies. Internally it uses:

- [Bubble Tea](https://github.com/charmbracelet/bubbletea) — terminal UI framework
- [Lipgloss](https://github.com/charmbracelet/lipgloss) — TUI styling
- [client-go](https://github.com/kubernetes/client-go) — cluster version & node detection
- [GitHub Releases API](https://api.github.com/repos/aws/karpenter-provider-aws/releases) — live version discovery
- Embedded compatibility matrix sourced from [karpenter.sh/docs/upgrading/compatibility](https://karpenter.sh/docs/upgrading/compatibility/)

Memory footprint is kept under 128 MiB at all times via `GOMEMLIMIT` and a bounded worker pool.

## Contributing

See [CONTRIBUTING.md](CONTRIBUTING.md). The goal is to contribute this to the Karpenter
community tools list and eventually propose it to the AWS containers roadmap.

## License

Apache 2.0
