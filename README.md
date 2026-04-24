<p align="center">
  <img src="https://raw.githubusercontent.com/kemilad/karpx/main/internal/ui/static/karpx-logo.svg" alt="karpx" width="400"/>
</p>

> ⚡ The Kubernetes essentials toolkit

```
  ██╗  ██╗ █████╗ ██████╗ ██████╗ ██╗  ██╗
  ██║ ██╔╝██╔══██╗██╔══██╗██╔══██╗╚██╗██╔╝
  █████╔╝ ███████║██████╔╝██████╔╝ ╚███╔╝
  ██╔═██╗ ██╔══██║██╔══██╗██╔═══╝  ██╔██╗
  ██║  ██╗██║  ██║██║  ██║██║     ██╔╝ ██╗
  ╚═╝  ╚═╝╚═╝  ╚═╝╚═╝  ╚═╝╚═╝     ╚═╝  ╚═╝
```

A single-binary toolkit to deploy and manage Kubernetes essentials — autoscaling, observability, GitOps, networking, and more — across your clusters. No YAML, no context-switching, just your terminal.

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

## AWS Prerequisites (EKS only)

> **Skip this section if you are using Azure AKS or GCP GKE** — those providers
> handle node identity differently and do not need manual IAM setup.

Karpenter on AWS needs two IAM roles and several AWS resources to exist
**before** you run `karpx install`. Without them the install appears to
succeed but nodes never provision (you will see `AccessDenied: iam:PassRole`
in Karpenter's logs).

| Resource | Purpose |
|----------|---------|
| **Controller IAM role** | Assumed by Karpenter's pod via IRSA. ARN passed to `karpx install --role-arn`. |
| **Node IAM role** | Assumed by EC2 instances Karpenter launches. Name set in `EC2NodeClass spec.role`. |
| **SQS interruption queue** | Receives Spot interruption / rebalance events for graceful draining. |
| **Subnet discovery tags** | `karpenter.sh/discovery: <cluster-name>` on every subnet Karpenter may use. |
| **Security group discovery tags** | Same tag on the node security group. |
| **OIDC provider** | Required for IRSA — lets Karpenter pods assume the controller role. |

### Quickest path — Terraform (recommended)

The `terraform/` directory in this repo contains a ready-to-use module that
creates everything above for a **new** EKS cluster (VPC, cluster, managed
node group for system pods, Karpenter roles, SQS queue, tags):

```bash
git clone https://github.com/kemilad/karpx
cd karpx/terraform

# 1. Set your cluster name and region
cat > terraform.tfvars <<EOF
cluster_name = "my-cluster"
region       = "us-east-1"
EOF

# 2. Create all resources (~5 min)
terraform init
terraform apply

# 3. Configure kubectl
aws eks update-kubeconfig --region us-east-1 --name my-cluster
```

After `terraform apply` the outputs show everything you need:

```
karpenter_controller_role_arn = "arn:aws:iam::123456789012:role/my-cluster-karpenter-controller"
karpenter_node_role_name      = "my-cluster-karpenter-node"
karpx_install_command         = "karpx install --provider aws -c my-cluster ..."
```

Copy the `karpx_install_command` output and run it — karpx will use the
controller role ARN to configure the Helm values for IRSA automatically.

When you later generate a **NodePool manifest** in karpx (`karpx ui` →
Node Provisioning, or `karpx nodes`), enter the **node role name**
(`my-cluster-karpenter-node`) in the "Karpenter Node Role" field — **not**
the controller role ARN.

### If you already have an EKS cluster

If your cluster exists but the Karpenter IAM roles do not, apply only the IAM
portion of the Terraform module:

```bash
# From karpx/terraform — target only the IAM + SQS resources
terraform apply \
  -target=aws_iam_role.karpenter_node \
  -target=aws_iam_role.karpenter_controller \
  -target=aws_iam_role_policy.karpenter_controller \
  -target=aws_iam_role_policy_attachment.karpenter_node_worker \
  -target=aws_iam_role_policy_attachment.karpenter_node_ecr \
  -target=aws_iam_role_policy_attachment.karpenter_node_cni \
  -target=aws_iam_role_policy_attachment.karpenter_node_ssm \
  -target=aws_iam_instance_profile.karpenter_node \
  -target=aws_eks_access_entry.karpenter_node \
  -target=aws_sqs_queue.karpenter_interruption \
  -target=aws_sqs_queue_policy.karpenter_interruption \
  -target=aws_cloudwatch_event_rule.spot_interruption \
  -target=aws_cloudwatch_event_rule.rebalance \
  -target=aws_cloudwatch_event_rule.instance_state
```

Then tag your existing subnets and node security group for discovery:

```bash
# Tag each subnet Karpenter may use
aws ec2 create-tags \
  --resources subnet-xxxx subnet-yyyy \
  --tags Key=karpenter.sh/discovery,Value=my-cluster

# Tag the node security group
aws ec2 create-tags \
  --resources sg-xxxx \
  --tags Key=karpenter.sh/discovery,Value=my-cluster
```

> **Common mistake:** using the controller role ARN in `EC2NodeClass spec.role`.
> That field must be the **node** role *name* (e.g. `my-cluster-karpenter-node`),
> not the controller role ARN. The node role is what EC2 instances assume;
> the controller role is what Karpenter's pod assumes.

## Usage

### Interactive TUI

```bash
karpx                            # uses your current kubeconfig context
karpx -c my-eks-prod             # target a specific cluster
karpx -c my-eks-prod -r ap-southeast-1
```

### Web dashboard

```bash
karpx ui                         # opens http://localhost:7654 in your browser
karpx ui -c my-eks-prod          # single-cluster view
karpx ui --port 9000             # custom port
```

The dashboard shows all kubeconfig contexts with their cloud provider, Kubernetes
version, Karpenter status, and compatibility badges. It auto-refreshes every 30 s.
Stop it with `Ctrl+C`.

### TUI keyboard shortcuts

| Key | Action |
|-----|--------|
| `↑` `↓` / `j` `k` | Move between clusters |
| `i` | Install Karpenter on selected cluster |
| `u` | Upgrade Karpenter on selected cluster |
| `n` | Manage NodePools / EC2NodeClasses |
| `a` | Open Add-ons panel for selected cluster |
| `r` | Refresh cluster list |
| `Esc` | Go back |
| `q` | Quit |

### Web dashboard install button

The `karpx ui` dashboard shows an **Install** button in the Actions column
for clusters where Karpenter is not yet installed:

| Provider | Button behaviour |
|----------|-----------------|
| **AWS EKS** | Shows the latest compatible version; clicking runs `helm install` automatically. If version resolution fails (e.g. GitHub rate-limit), the button still appears and prompts you to enter a version. |
| **Azure AKS** | Shows a **Setup Guide →** link to the Microsoft AKS Karpenter docs. |
| **GCP GKE** | Shows a **Setup Guide →** link to the GKE Karpenter provider docs. |

### Node type optimisation

`karpx nodes` analyses your running workloads and asks one question:

```
  What is your node provisioning priority?

    [1]  Cost-Optimized   — Spot + Graviton (arm64), saves 60-80% vs on-demand
    [2]  Balanced         — Mixed Spot + On-Demand, multiple instance families
    [3]  High-Performance — On-Demand only, latest-gen, no interruptions
    [4]  Free-Tier        — Free-tier eligible instances only (m7i-flex, c7i-flex, t3, t4g)
```

It then generates and optionally applies a Karpenter **NodePool + NodeClass** manifest
tuned to your actual workload profile (CPU/memory ratio, GPU usage, batch jobs).

| Mode | Capacity | Instance families | Best for |
|------|----------|-------------------|----------|
| `cost` | Spot + On-Demand | c7g, c6g, c7i, m7i, … | Fault-tolerant workloads, lowest spend |
| `balanced` | Spot + On-Demand | m7g, m7i, c7g, c7i, … | Most production workloads |
| `performance` | On-Demand only | m7i, c7i, m6i, c6i, … | Latency-sensitive / stateful services |
| `freetier` | On-Demand only | m7i-flex, c7i-flex, t3, t3a, t4g | AWS accounts with free-tier or instance-type restrictions |

```bash
karpx nodes -c my-cluster                    # interactive: analyse + ask + apply/save
karpx nodes -c my-cluster --mode cost        # skip the question, use cost-optimised
karpx nodes -c my-cluster --mode freetier    # free-tier eligible instances only
```

### Estimated cost

After analysing your workloads, karpx shows an approximate cost projection alongside the instance recommendation — no extra commands needed.

```
  Estimated cost
  Primary instance  : m7g.xlarge  (4 vCPU, 16 GiB)
  Estimated nodes   : 5  (based on current workload + 20% headroom)

  On-demand         : $0.1632/hr per node   ~$596/mo total
  Spot (typical)    : $0.0571/hr per node   ~$209/mo total   (saves ~65%)

  Note: Approximate us-east-1 on-demand pricing. Actual costs vary by region and Spot availability.
```

The same cost breakdown appears as a card in the web dashboard (`karpx ui`) when you generate a NodePool manifest, with on-demand and Spot figures shown side by side.

Pricing covers all AWS instance families used in recommendations: m7i, m7g, c7i, c7g, r7i, r7g, t3, t4g, g4dn, g5, p3, and their variants. Cost estimation is currently available for AWS only.

### Open-source add-ons

karpx includes a built-in add-ons manager to install, inspect, and remove popular
open-source tools into any cluster via Helm — no separate `helm add repo` or
`kubectl apply` commands needed.

#### Available add-ons

| ID | Name | Category | What it does |
|----|------|----------|-------------|
| `loki-stack` | Logging Stack | Logging | Loki + Promtail + Grafana — log aggregation and live exploration |
| `kube-prometheus-stack` | Monitoring Stack | Monitoring | Grafana + Prometheus + Node Exporter — metrics and dashboards |
| `aws-load-balancer-controller` | AWS Load Balancer Controller | Networking | Provision AWS ALB/NLB for Kubernetes Services and Ingresses |
| `keda` | KEDA | Autoscaling | Kubernetes Event-Driven Autoscaling — scale on queues, topics, and more |
| `cert-manager` | cert-manager | Security | Automatic TLS certificate provisioning via Let's Encrypt / ACME |
| `argocd` | Argo CD | GitOps | Declarative GitOps continuous delivery — deploy and sync apps from Git |

#### Shared Grafana — no duplicate instances

When both `loki-stack` and `kube-prometheus-stack` are installed, karpx
automatically disables the duplicate Grafana so only **one** Grafana instance
runs in the `monitoring` namespace:

- Installing **kube-prometheus-stack** when loki-stack is already present → karpx
  upgrades loki-stack with `grafana.enabled=false` automatically.
- Installing **loki-stack** when kube-prometheus-stack is already present →
  loki-stack is installed without its own Grafana from the start.

In both cases, all logs (Loki) and metrics (Prometheus) are visible through the
**single shared Grafana** from `kube-prometheus-stack`.

#### Pre-provisioned Grafana dashboards

karpx pre-provisions the following community dashboards into Grafana at install
time — they appear automatically without any manual import:

| Dashboard | Grafana ID | Installed with |
|-----------|-----------|----------------|
| Loki Logs Explorer | [13639](https://grafana.com/grafana/dashboards/13639) | `loki-stack` or `kube-prometheus-stack` (when Loki is also present) |
| Promtail 2.x (scrape targets, bytes/s, entry rate per pod) | [15443](https://grafana.com/grafana/dashboards/15443) | `loki-stack` or `kube-prometheus-stack` (when Loki is also present) |

#### Grafana access after install

After installing any observability add-on, karpx prints a ready-to-use
port-forward command and URL:

```
  ─── Grafana ─────────────────────────────────────────────────────
  Port-forward:  kubectl port-forward -n monitoring svc/kube-prometheus-stack-grafana 3000:80
  URL:           http://localhost:3000
  Credentials:   admin / prom-operator
  ─────────────────────────────────────────────────────────────────
```

Open `http://localhost:3000` in your browser to explore logs and metrics.

In the **web dashboard** (`karpx ui`), the add-ons table shows a **📊 Grafana** button
next to each installed observability stack. Clicking it starts the port-forward
automatically and opens Grafana in a new tab.

#### Argo CD

`argocd` installs the full Argo CD server with the web UI enabled. The server runs
in HTTP (insecure) mode so the port-forward works over plain HTTP.

After install, karpx prints access details:

```
  ─── Argo CD ─────────────────────────────────────────────────────
  Port-forward:  kubectl port-forward -n argocd svc/argocd-server 8080:80
  URL:           http://localhost:8080
  Credentials:   admin / kubectl -n argocd get secret argocd-initial-admin-secret \
                   -o jsonpath='{.data.password}' | base64 -d
  ─────────────────────────────────────────────────────────────────
```

In the **web dashboard**, a **🚀 Argo CD** button appears next to the installed addon.
Clicking it starts the port-forward on `localhost:8080` and opens the Argo CD UI.

**Argo CD CLI** — install the `argocd` CLI alongside the server to manage apps from
your terminal:

```bash
# macOS (Homebrew)
brew install argocd

# Linux
curl -sSL -o /usr/local/bin/argocd \
  https://github.com/argoproj/argo-cd/releases/latest/download/argocd-linux-amd64
chmod +x /usr/local/bin/argocd
```

Log in after starting the port-forward:

```bash
argocd login localhost:8080 --insecure --username admin \
  --password $(kubectl -n argocd get secret argocd-initial-admin-secret \
              -o jsonpath='{.data.password}' | base64 -d)
```

#### cert-manager

`cert-manager` is installed with `installCRDs=true` so the required
`Certificate`, `Issuer`, and `ClusterIssuer` CRDs are created automatically —
no separate `kubectl apply` for CRDs needed.

#### AWS Load Balancer Controller — automatic cluster discovery

karpx automatically derives the values that the controller needs from your
kubeconfig context — you do not need to pass them manually:

| Value | How it is resolved |
|-------|-------------------|
| `clusterName` | Extracted from the EKS ARN in the kubeconfig context (e.g. `arn:aws:eks:<region>:<account>:cluster/<name>`) |
| `region` | Extracted from the same EKS ARN |
| `vpcId` | Queried live from the EKS API via `aws eks describe-cluster` |

After install, annotate the ServiceAccount with your IAM role ARN so the
controller can provision ALBs and NLBs:

```bash
kubectl annotate serviceaccount -n kube-system aws-load-balancer-controller \
  eks.amazonaws.com/role-arn=arn:aws:iam::<ACCOUNT_ID>:role/<ROLE_NAME>
```

See the [official setup guide](https://kubernetes-sigs.github.io/aws-load-balancer-controller/)
for instructions on creating the required IAM policy and role.

#### CLI usage

```bash
# List all add-ons and their status on a cluster
karpx addons list -c my-cluster

# Install observability stack (Grafana is shared automatically)
karpx addons install kube-prometheus-stack -c my-cluster
karpx addons install loki-stack -c my-cluster

# Install networking add-on (cluster name, region, VPC ID are derived from context automatically)
karpx addons install aws-load-balancer-controller -c my-cluster

# Install GitOps (Argo CD web UI + CLI support)
karpx addons install argocd -c my-cluster

# Install other add-ons
karpx addons install keda -c my-cluster
karpx addons install cert-manager -c my-cluster

# Uninstall an add-on
karpx addons uninstall loki-stack -c my-cluster
```

#### TUI usage

From the cluster list, press `a` to open the **Add-ons** panel for the selected cluster.

| Key | Action |
|-----|--------|
| `↑` `↓` / `j` `k` | Move between add-ons |
| `i` | Install selected add-on |
| `x` | Uninstall selected add-on |
| `r` | Refresh add-on status |
| `Esc` | Go back |

Each add-on row shows its name, category, description, installed version (if any),
and a status badge (`● INSTALLED` / `○ NOT INSTALLED`). Selecting a row reveals a
detail panel with the Helm chart reference, target namespace, and release name.

Install uses `helm upgrade --install` with `--wait` (10 min timeout), so progress
streams live to your terminal and the command exits with a non-zero status on failure.

After a successful install, access details are printed directly in the terminal:

- **Grafana** — port-forward command + `http://localhost:3000` + credential hint
- **Argo CD** — port-forward command + `http://localhost:8080` + command to retrieve the initial admin password

#### Web dashboard URL column

The add-ons table in `karpx ui` has a **URL** column that shows endpoint buttons
for installed add-ons:

| Button | Addon | What it does |
|--------|-------|-------------|
| 📊 Grafana | `loki-stack`, `kube-prometheus-stack` | Starts `kubectl port-forward` to `localhost:3000` and opens Grafana |
| 🚀 Argo CD | `argocd` | Starts `kubectl port-forward` to `localhost:8080` and opens the Argo CD web UI |

The port-forward is started in the background by the dashboard server and kept alive
until the dashboard is stopped (`Ctrl+C`). Clicking the button again reuses the
existing port-forward if the port is already open.

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

# Uninstall Karpenter from a cluster.
karpx uninstall -c my-cluster

# Uninstall and also delete the Karpenter namespace.
karpx uninstall -c my-cluster --delete-namespace

# Analyse workloads and generate an optimised NodePool manifest.
karpx nodes -c my-cluster
karpx nodes -c my-cluster --mode cost        # cost-optimised (Spot + Graviton)
karpx nodes -c my-cluster --mode performance # high-performance (on-demand)
karpx nodes -c my-cluster --mode freetier   # free-tier eligible instances only

# List NodePools.
karpx nodepools -c my-cluster
karpx np -c my-cluster          # short alias

# Print karpx version.
karpx version

# List add-ons and their install status.
karpx addons list -c my-cluster

# Install add-ons (streams helm output; exits non-zero on failure).
karpx addons install loki-stack              -c my-cluster
karpx addons install kube-prometheus-stack   -c my-cluster
karpx addons install aws-load-balancer-controller -c my-cluster
karpx addons install argocd                  -c my-cluster
karpx addons install keda                    -c my-cluster
karpx addons install cert-manager            -c my-cluster

# Uninstall an add-on.
karpx addons uninstall loki-stack -c my-cluster
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

## Testing Karpenter before going to production

Before rolling out Karpenter on a production cluster, validate that node
provisioning, scaling, and consolidation all work correctly using the
included load-test manifest.

### Quick start

```bash
# 1. Apply the load test (50 pods, 500m CPU + 512Mi memory each)
kubectl apply -f https://raw.githubusercontent.com/kemilad/karpx/main/karpx-load-test.yaml

# 2. Watch Karpenter provision new nodes in real time
kubectl get nodes -w

# 3. Watch pods schedule onto the new nodes
kubectl get pods -l app=karpx-load-test -w

# 4. Inspect Karpenter's provisioning decisions
kubectl logs -n karpenter -l app.kubernetes.io/name=karpenter -f \
  | grep -E "provisioned|launched|NodeClaim|nodeclaim"

# 5. Clean up — Karpenter consolidates and terminates the nodes automatically
kubectl delete -f https://raw.githubusercontent.com/kemilad/karpx/main/karpx-load-test.yaml
kubectl get nodes -w   # watch nodes drain and terminate (~1–2 min)
```

### What to expect

| Stage | What happens | Typical time |
|-------|-------------|-------------|
| Apply | 50 pods go `Pending` — no capacity on existing nodes | instant |
| Provisioning | Karpenter evaluates instance types and creates NodeClaims | ~10–20 s |
| Node ready | New EC2 nodes appear (`NotReady` → `Ready`) | ~45–90 s |
| Pods running | All 50 pods schedule and go `Running` | ~15 s after node ready |
| Delete | Pods removed, Karpenter drains and terminates nodes | ~60–90 s |

With the default `500m` CPU / `512Mi` memory per pod, 50 replicas require
roughly **25 CPU cores** — on `t3.medium` (2 vCPU) that means ~13 new nodes,
giving you a clear picture of Karpenter's bin-packing and spot-selection logic.

### Scale up to stress test further

```bash
# Double the load
kubectl scale deployment karpx-load-test --replicas=100

# Watch Karpenter add more nodes to handle the extra demand
kubectl get nodes -w
```

### Validate consolidation

After deleting some pods, Karpenter should consolidate workloads onto fewer
nodes and terminate the now-empty ones (consolidation policy is set by your
NodePool — `WhenEmptyOrUnderutilized` for Balanced/Cost, `WhenEmpty` for
High-Performance):

```bash
# Scale down to 10 pods and watch nodes consolidate
kubectl scale deployment karpx-load-test --replicas=10
kubectl get nodes -w
```

### Pre-production checklist

Before promoting Karpenter to production verify:

- [ ] New nodes appear within 90 seconds of pods going `Pending`
- [ ] All 50 pods reach `Running` state
- [ ] Node labels include `karpenter.sh/nodepool: karpx-default`
- [ ] After cleanup, all Karpenter-provisioned nodes terminate (no orphaned nodes)
- [ ] Karpenter controller logs show no `ERROR` lines during the test
- [ ] `kubectl get nodeclaims` shows claims created and then deleted cleanly

```bash
# Quick health check — should show no errors
kubectl logs -n karpenter -l app.kubernetes.io/name=karpenter \
  --since=10m | grep ERROR
```

## Uninstall karpx

### Homebrew

```bash
brew uninstall karpx
brew untap kemilad/tap   # optional — removes the tap entirely
```

### curl / manual install

```bash
rm "$(which karpx)"
```

### go install

```bash
rm "$(go env GOPATH)/bin/karpx"
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
