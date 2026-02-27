# karpx ⚡

> Karpenter for EKS — managed from your terminal

```
  ██╗  ██╗ █████╗ ██████╗ ██████╗ ██╗  ██╗
  ██║ ██╔╝██╔══██╗██╔══██╗██╔══██╗╚██╗██╔╝
  █████╔╝ ███████║██████╔╝██████╔╝ ╚███╔╝ 
  ██╔═██╗ ██╔══██║██╔══██╗██╔═══╝  ██╔██╗ 
  ██║  ██╗██║  ██║██║  ██║██║     ██╔╝ ██╗
  ╚═╝  ╚═╝╚═╝  ╚═╝╚═╝  ╚═╝╚═╝     ╚═╝  ╚═╝
```

A single-binary TUI to install, upgrade, and configure [Karpenter](https://karpenter.sh)
across all your EKS clusters — no YAML, no context-switching, just your terminal.

## Install

### Homebrew (macOS / Linux) — recommended

```bash
brew tap your-org/tap
brew install karpx
```

### curl installer

```bash
curl -fsSL https://raw.githubusercontent.com/your-org/karpx/main/scripts/install.sh | bash
```

Override version or install directory:

```bash
VERSION=v0.2.0 INSTALL_DIR=~/.local/bin \
  curl -fsSL .../install.sh | bash
```

### go install

```bash
go install github.com/your-org/karpx@latest
```

### Manual

Download the binary for your platform from [Releases](https://github.com/your-org/karpx/releases),
extract it, and place it on your `$PATH`.

---

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

### Non-interactive (CI / scripting)

```bash
# Print the installed Karpenter version.
karpx detect -c my-cluster

# Install without TUI.
karpx install \
  -c my-cluster \
  --cluster-name my-cluster \
  -r ap-southeast-1 \
  --role-arn arn:aws:iam::123456789012:role/KarpenterController

# Upgrade to the latest compatible version.
karpx upgrade -c my-cluster

# Upgrade to a specific version.
karpx upgrade -c my-cluster --version v1.3.0

# List NodePools.
karpx nodepools -c my-cluster
karpx np -c my-cluster          # short alias

# Print karpx version.
karpx version
```

## Requirements

- `kubectl` configured (`~/.kube/config`) with your EKS cluster contexts
- AWS credentials (environment variables, `~/.aws/credentials`, or IAM instance role)
- Helm is **not** required separately — karpx uses the Helm Go library internally

## How it works

karpx is a single static binary with zero runtime dependencies. Internally it uses:

- [Bubble Tea](https://github.com/charmbracelet/bubbletea) — terminal UI framework
- [Lipgloss](https://github.com/charmbracelet/lipgloss) — TUI styling
- [Helm v3 library](https://pkg.go.dev/helm.sh/helm/v3) — programmatic install/upgrade
- [client-go](https://github.com/kubernetes/client-go) — NodePool/EC2NodeClass CRD operations
- [AWS SDK v2](https://github.com/aws/aws-sdk-go-v2) — EC2 instance type discovery

Memory footprint is kept under 128 MiB at all times via `GOMEMLIMIT` and a bounded worker pool.

## Contributing

See [CONTRIBUTING.md](CONTRIBUTING.md). The goal is to contribute this to the Karpenter
community tools list and eventually propose it to the AWS containers roadmap.

## License

Apache 2.0
