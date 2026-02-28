# Contributing to karpx

Thank you for your interest in contributing! This document covers everything
you need to get started.

## Table of contents

- [Code of conduct](#code-of-conduct)
- [Getting started](#getting-started)
- [Development setup](#development-setup)
- [Project structure](#project-structure)
- [Making changes](#making-changes)
- [Commit conventions](#commit-conventions)
- [Opening a pull request](#opening-a-pull-request)
- [Reporting bugs](#reporting-bugs)
- [Requesting features](#requesting-features)

## Code of conduct

This project follows the [CNCF Code of Conduct](https://github.com/cncf/foundation/blob/main/code-of-conduct.md).
Be respectful, inclusive, and constructive in all interactions.

## Getting started

1. **Fork** the repository on GitHub.
2. **Clone** your fork locally:
   ```bash
   git clone https://github.com/<your-username>/karpx.git
   cd karpx
   ```
3. Add the upstream remote:
   ```bash
   git remote add upstream https://github.com/kemilad/karpx.git
   ```

## Development setup

**Prerequisites**

| Tool | Version | Purpose |
|---|---|---|
| Go | ≥ 1.22 | Build the binary |
| kubectl | any | Cluster interaction |
| helm | ≥ 3 | Karpenter detection |
| make | any | Build shortcuts |

**Install dependencies and build**

```bash
go mod tidy
make build          # outputs ./bin/karpx
```

**Run from source**

```bash
make run                          # uses current kubeconfig context
make run ARGS="-c my-cluster"     # target a specific context
```

**Run tests**

```bash
make test
```

**Cross-platform release artifacts**

```bash
make release        # builds linux/darwin amd64+arm64 into ./dist/
```

## Project structure

```
karpx/
├── main.go                      # CLI entry point (cobra commands)
├── Makefile
├── install.sh                   # curl-based karpx installer
├── karpx.rb                     # Homebrew formula
├── go.mod
└── internal/
    ├── compat/
    │   └── matrix.go            # Karpenter ↔ Kubernetes compatibility matrix
    ├── helm/
    │   └── detect.go            # Karpenter installation detection via helm
    ├── kube/
    │   ├── provider.go          # Cloud provider auto-detection
    │   ├── version.go           # Cluster Kubernetes version
    │   └── workload.go          # Running workload analysis
    ├── nodes/
    │   ├── selector.go          # Node type recommendation engine
    │   └── manifest.go          # NodePool + NodeClass YAML generation
    └── tui/
        ├── model.go             # Root BubbleTea model + navigation
        ├── nav.go               # Navigation messages and targets
        ├── dashboard.go         # Cluster dashboard view
        └── styles.go            # Colour palette and component styles
```

## Making changes

1. Create a branch from `main`:
   ```bash
   git checkout -b feat/my-feature
   ```
2. Make your changes. Keep commits focused — one logical change per commit.
3. Add or update tests where relevant.
4. Run the test suite before pushing:
   ```bash
   make test
   ```
5. Build and smoke-test the binary:
   ```bash
   make build
   ./bin/karpx --help
   ```

### Areas where contributions are especially welcome

- **Helm install/upgrade wiring** — the CLI stubs in `main.go` (`runInstallAWS`, `runUpgrade`) need the actual `helm.sh/helm/v3` library calls
- **Azure AKS full install flow** — mirroring the AWS step-by-step wizard
- **GCP GKE full install flow**
- **TUI install / upgrade views** — replacing the "coming soon" placeholders in `internal/tui/model.go`
- **NodePool / EC2NodeClass apply via client-go** — instead of shelling out to kubectl
- **Compatibility matrix updates** — when new Karpenter versions ship, update `internal/compat/matrix.go`
- **Tests** — unit tests for `compat`, `nodes/selector`, and `kube/workload` packages

## Commit conventions

Follow [Conventional Commits](https://www.conventionalcommits.org/):

```
<type>(<scope>): <short summary>

[optional body]
```

| Type | When to use |
|---|---|
| `feat` | New feature |
| `fix` | Bug fix |
| `docs` | Documentation only |
| `refactor` | Code restructure, no behaviour change |
| `test` | Adding or updating tests |
| `chore` | Build scripts, deps, tooling |

**Examples**

```
feat(nodes): add Fargate profile support to node selector
fix(compat): correct k8s max version for Karpenter 0.37.x
docs: update Azure AKS install instructions
```

## Opening a pull request

1. Push your branch to your fork:
   ```bash
   git push origin feat/my-feature
   ```
2. Open a pull request against `kemilad/karpx` `main`.
3. Fill in the PR template:
   - **What** changed and **why**
   - How to test it
   - Any follow-up work
4. A maintainer will review within a few days. Address feedback with new commits (do not force-push during review).

## Reporting bugs

Open an issue at [github.com/kemilad/karpx/issues](https://github.com/kemilad/karpx/issues) and include:

- karpx version (`karpx version`)
- Kubernetes version and cloud provider
- Steps to reproduce
- Expected vs actual behaviour
- Relevant error output or logs

## Requesting features

Open an issue with the `enhancement` label. Describe:

- The problem you are trying to solve
- Your proposed solution (if you have one)
- Any alternatives you considered

For larger changes, open an issue for discussion **before** submitting a PR.

## License

By contributing, you agree that your contributions will be licensed under the
[Apache 2.0 License](LICENSE).
