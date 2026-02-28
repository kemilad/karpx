package main

import (
	"bufio"
	"fmt"
	"os"
	"runtime"
	"runtime/debug"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/spf13/cobra"

	"github.com/kemilad/karpx/internal/compat"
	"github.com/kemilad/karpx/internal/helm"
	"github.com/kemilad/karpx/internal/kube"
	"github.com/kemilad/karpx/internal/tui"
)

var version = "dev"

const banner = `
  ██╗  ██╗ █████╗ ██████╗ ██████╗ ██╗  ██╗
  ██║ ██╔╝██╔══██╗██╔══██╗██╔══██╗╚██╗██╔╝
  █████╔╝ ███████║██████╔╝██████╔╝ ╚███╔╝
  ██╔═██╗ ██╔══██║██╔══██╗██╔═══╝  ██╔██╗
  ██║  ██╗██║  ██║██║  ██║██║     ██╔╝ ██╗
  ╚═╝  ╚═╝╚═╝  ╚═╝╚═╝  ╚═╝╚═╝     ╚═╝  ╚═╝
`

func main() {
	runtime.GOMAXPROCS(2)
	debug.SetMemoryLimit(128 << 20)
	debug.SetGCPercent(200)

	if err := rootCmd().Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func rootCmd() *cobra.Command {
	var kubeCtx string
	var region  string

	root := &cobra.Command{
		Use:   "karpx",
		Short: "Karpenter manager — works with AWS EKS, Azure AKS, and GCP GKE",
		Long: banner + `  ⚡ Karpenter — managed from your terminal

  Open the interactive TUI to install, upgrade, and configure
  Karpenter across all your Kubernetes clusters.

  Supported platforms:
    AWS EKS    — fully supported  (karpenter-provider-aws)
    Azure AKS  — preview          (karpenter-provider-azure-aks)
    GCP GKE    — experimental     (karpenter-provider-gcp)
    On-prem    — not supported by Karpenter

  Examples:
    karpx                                  open TUI (current context)
    karpx --context staging                target a specific cluster
    karpx --context prod --region us-east-1

  Run 'karpx <command> --help' for non-interactive usage.
`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runTUI(kubeCtx, region)
		},
	}

	root.PersistentFlags().StringVarP(&kubeCtx, "context", "c", "", "kubeconfig context (default: current context)")
	root.PersistentFlags().StringVarP(&region,  "region",  "r", "", "AWS region (default: from AWS config)")
	root.SilenceUsage = true

	root.AddCommand(detectCmd(), installCmd(), upgradeCmd(), nodePoolsCmd(), versionCmd())
	return root
}

func runTUI(kubeCtx, region string) error {
	m := tui.NewModel(tui.Config{KubeContext: kubeCtx, Region: region})
	p := tea.NewProgram(m, tea.WithAltScreen(), tea.WithMouseCellMotion())
	_, err := p.Run()
	return err
}

// ─────────────────────────────────────────────────────────────────────────────
// detect command
// ─────────────────────────────────────────────────────────────────────────────

func detectCmd() *cobra.Command {
	var kubeCtx string
	cmd := &cobra.Command{
		Use:     "detect",
		Short:   "Check cloud provider, Karpenter installation, and version compatibility",
		Example: "  karpx detect\n  karpx detect -c my-cluster",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runDetect(kubeCtx)
		},
	}
	cmd.Flags().StringVarP(&kubeCtx, "context", "c", "", "kubeconfig context")
	return cmd
}

func runDetect(kubeCtx string) error {
	fmt.Printf("\n  Checking cluster %s…\n\n", contextOrCurrent(kubeCtx))

	// ── Provider detection ────────────────────────────────────────────────
	fmt.Printf("  Detecting cloud provider…\n")
	provider := kube.DetectProvider(kubeCtx)
	meta := provider.Meta()
	fmt.Printf("  Provider            : %s", meta.Label)
	switch meta.SupportLevel {
	case "full":
		fmt.Printf("  (● full Karpenter support)\n")
	case "preview":
		fmt.Printf("  (◐ preview)\n")
	case "experimental":
		fmt.Printf("  (◌ experimental)\n")
	default:
		fmt.Printf("  (✗ no official Karpenter provider)\n")
	}

	if !provider.Supported() {
		printUnsupportedProvider()
		return nil
	}

	// ── Kubernetes version ────────────────────────────────────────────────
	k8sVer, err := kube.GetServerVersion(kubeCtx)
	if err != nil {
		fmt.Printf("  ✗ Could not reach cluster: %v\n\n", err)
		return err
	}
	fmt.Printf("  Kubernetes version  : %s\n", k8sVer)

	// ── Karpenter detection ───────────────────────────────────────────────
	info, err := helm.DetectKarpenter(kubeCtx)
	if err != nil {
		fmt.Printf("  ✗ Detection error: %v\n\n", err)
		return err
	}

	if !info.Installed {
		fmt.Printf("  Karpenter           : not installed\n")
	} else {
		fmt.Printf("  Karpenter version   : %s\n", info.Version)
		fmt.Printf("  Namespace           : %s\n", info.Namespace)

		// Compatibility is defined for AWS only (other providers have their own matrices).
		if provider == kube.ProviderAWS {
			if compat.IsCompatible(info.Version, k8sVer) {
				fmt.Printf("  Compatibility       : ✓  compatible with Kubernetes %s\n", k8sVer)
			} else {
				fmt.Printf("  Compatibility       : ✗  NOT compatible with Kubernetes %s\n", k8sVer)
			}
		}
	}

	// ── Latest compatible version ─────────────────────────────────────────
	if provider == kube.ProviderAWS {
		fmt.Printf("\n  Fetching latest compatible version from GitHub…\n")
		latest, all, err := compat.LatestCompatible(k8sVer)
		if err != nil {
			fmt.Printf("  (could not fetch: %v)\n\n", err)
			return nil
		}
		if latest == "" {
			fmt.Printf("  No known compatible Karpenter version for Kubernetes %s\n\n", k8sVer)
			return nil
		}
		fmt.Printf("  Latest compatible   : v%s\n", latest)
		if len(all) > 1 {
			fmt.Printf("  All compatible      : %s\n", formatVersionList(all, 5))
		}

		if !info.Installed {
			fmt.Printf("\n  ► Run to install:\n")
			fmt.Printf("    karpx install -c <ctx> --cluster-name <name> --role-arn <arn>\n\n")
			return nil
		}

		installed := strings.TrimPrefix(info.Version, "v")
		if !compat.IsCompatible(installed, k8sVer) {
			fmt.Printf("\n  ✗ Installed Karpenter is incompatible — upgrade required.\n")
			fmt.Printf("  ► karpx upgrade -c %s --version v%s\n\n", contextOrCurrent(kubeCtx), latest)
		} else if installed != latest {
			fmt.Printf("\n  ▲ Upgrade available: v%s → v%s\n", installed, latest)
			fmt.Printf("  ► karpx upgrade -c %s --version v%s\n\n", contextOrCurrent(kubeCtx), latest)
		} else {
			fmt.Printf("\n  ✓  Karpenter is up to date.\n\n")
		}
	} else {
		// Non-AWS providers: point to their own docs.
		fmt.Printf("\n  Provider docs       : %s\n", meta.DocsURL)
		fmt.Printf("  Provider repo       : %s\n\n", meta.ProviderRepo)
	}

	return nil
}

// ─────────────────────────────────────────────────────────────────────────────
// install command — provider-aware with interactive questioning
// ─────────────────────────────────────────────────────────────────────────────

func installCmd() *cobra.Command {
	var kubeCtx, clusterName, region, roleARN, karpVer, intQueue, providerFlag string
	cmd := &cobra.Command{
		Use:   "install",
		Short: "Install Karpenter — detects cloud provider and guides through setup",
		Long: `Install Karpenter on a Kubernetes cluster.

karpx auto-detects your cloud provider and walks you through the required
configuration interactively. You can also pass all flags directly for CI use.

Supported providers:
  AWS EKS   — full support    (--provider aws)
  Azure AKS — preview         (--provider azure)
  GCP GKE   — experimental    (--provider gcp)
`,
		Example: `  # Interactive (karpx asks questions):
  karpx install -c my-cluster

  # Non-interactive AWS EKS:
  karpx install --provider aws \
    -c my-cluster \
    --cluster-name my-cluster \
    -r ap-southeast-1 \
    --role-arn arn:aws:iam::123456789:role/KarpenterController`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runInstall(kubeCtx, providerFlag, clusterName, region, roleARN, karpVer, intQueue)
		},
	}
	cmd.Flags().StringVarP(&kubeCtx,      "context",            "c", "", "kubeconfig context")
	cmd.Flags().StringVar(&providerFlag,   "provider",               "", "cloud provider: aws | azure | gcp (default: auto-detect)")
	cmd.Flags().StringVarP(&clusterName,  "cluster-name",       "n", "", "EKS cluster name (AWS only)")
	cmd.Flags().StringVarP(&region,       "region",             "r", "", "AWS region (AWS only)")
	cmd.Flags().StringVar(&roleARN,       "role-arn",               "", "Karpenter controller IAM role ARN (AWS only)")
	cmd.Flags().StringVar(&karpVer,       "version",                "", "Karpenter version (default: latest compatible)")
	cmd.Flags().StringVar(&intQueue,      "interruption-queue",     "", "SQS queue name for spot interruption (AWS, optional)")
	return cmd
}

func runInstall(kubeCtx, providerFlag, clusterName, region, roleARN, karpVer, intQueue string) error {
	printSection("Step 1: Detecting cloud provider")

	// ── Resolve provider ──────────────────────────────────────────────────
	var provider kube.Provider
	if providerFlag != "" {
		provider = kube.ParseProvider(providerFlag)
		fmt.Printf("  Provider (from --provider flag): %s\n", provider.Meta().Label)
	} else {
		fmt.Printf("  Auto-detecting from cluster context…\n")
		provider = kube.DetectProvider(kubeCtx)
		if provider != kube.ProviderUnknown {
			fmt.Printf("  Detected: %s\n", provider.Meta().Label)
		} else {
			fmt.Printf("  Could not auto-detect provider.\n")
			provider = askProviderMenu()
		}
	}

	meta := provider.Meta()

	// ── Handle unsupported providers ──────────────────────────────────────
	if !provider.Supported() {
		fmt.Println()
		printUnsupportedProvider()
		return nil
	}

	// ── Print provider support banner ─────────────────────────────────────
	fmt.Println()
	switch meta.SupportLevel {
	case "full":
		fmt.Printf("  ● %s — Karpenter is fully supported (production ready)\n", meta.Label)
	case "preview":
		fmt.Printf("  ◐ %s — Karpenter support is in preview\n", meta.Label)
		fmt.Printf("    Docs : %s\n", meta.DocsURL)
	case "experimental":
		fmt.Printf("  ◌ %s — Karpenter support is experimental (not recommended for production)\n", meta.Label)
		fmt.Printf("    Docs : %s\n", meta.DocsURL)
	}

	// ── Check if already installed ────────────────────────────────────────
	fmt.Println()
	printSection("Step 2: Checking existing installation")
	existingInfo, _ := helm.DetectKarpenter(kubeCtx)
	if existingInfo.Installed {
		fmt.Printf("  Karpenter v%s is already installed in namespace %s.\n",
			existingInfo.Version, existingInfo.Namespace)
		k8sVer, err := kube.GetServerVersion(kubeCtx)
		if err == nil && provider == kube.ProviderAWS {
			if !compat.IsCompatible(existingInfo.Version, k8sVer) {
				fmt.Printf("  ✗ Installed version is NOT compatible with Kubernetes %s.\n", k8sVer)
				if latest, _, _ := compat.LatestCompatible(k8sVer); latest != "" {
					fmt.Printf("  ▲ Latest compatible: v%s\n", latest)
				}
			} else {
				fmt.Printf("  ✓  Compatible with Kubernetes %s.\n", k8sVer)
			}
		}
		fmt.Printf("\n  Run `karpx upgrade` instead.\n\n")
		return nil
	}
	fmt.Printf("  Karpenter is not installed — proceeding.\n")

	// ── Provider-specific install flow ────────────────────────────────────
	switch provider {
	case kube.ProviderAWS:
		return runInstallAWS(kubeCtx, clusterName, region, roleARN, karpVer, intQueue)
	case kube.ProviderAzure:
		return runInstallAzure(kubeCtx, karpVer)
	case kube.ProviderGCP:
		return runInstallGCP(kubeCtx, karpVer)
	}
	return nil
}

// ── AWS EKS install flow ──────────────────────────────────────────────────────

func runInstallAWS(kubeCtx, clusterName, region, roleARN, karpVer, intQueue string) error {
	fmt.Println()
	printSection("Step 3: Cluster information (AWS EKS)")

	clusterName = askIfEmpty(clusterName, "EKS cluster name", "")
	if clusterName == "" {
		return fmt.Errorf("cluster name is required for AWS EKS installation")
	}

	region = askIfEmpty(region, "AWS region", "us-east-1")

	fmt.Println()
	printSection("Step 4: IAM configuration")
	fmt.Printf("  Karpenter needs an IAM role to manage EC2 instances.\n")
	fmt.Printf("  Create one at: https://karpenter.sh/docs/getting-started/getting-started-with-karpenter/#create-the-karpentercontroller-iam-role\n\n")

	roleARN = askIfEmpty(roleARN, "Karpenter controller IAM role ARN", "")
	if roleARN == "" {
		return fmt.Errorf("IAM role ARN is required for AWS EKS installation")
	}

	intQueue = askIfEmpty(intQueue, "SQS interruption queue name (press Enter to skip)", "")

	fmt.Println()
	printSection("Step 5: Karpenter version")

	k8sVer, err := kube.GetServerVersion(kubeCtx)
	if err != nil {
		fmt.Printf("  ✗ Could not get cluster Kubernetes version: %v\n", err)
		fmt.Printf("    Specify --version to override.\n\n")
		return err
	}
	fmt.Printf("  Kubernetes version  : %s\n", k8sVer)

	if karpVer == "" {
		fmt.Printf("  Fetching latest compatible Karpenter version from GitHub…\n")
		latest, all, err := compat.LatestCompatible(k8sVer)
		if err != nil || latest == "" {
			return fmt.Errorf("could not resolve latest Karpenter version: %v", err)
		}
		fmt.Printf("  Latest compatible   : v%s\n", latest)
		if len(all) > 1 {
			fmt.Printf("  All compatible      : %s\n", formatVersionList(all, 5))
		}
		fmt.Println()
		if confirmDefaultPrompt(fmt.Sprintf("  Use Karpenter v%s? [Y/n] ", latest)) {
			karpVer = "v" + latest
		} else {
			karpVer = askIfEmpty("", "Karpenter version to install", "v"+latest)
		}
	}

	// ── Summary + confirm ─────────────────────────────────────────────────
	fmt.Println()
	printSection("Summary")
	fmt.Printf("  Provider        : AWS EKS\n")
	fmt.Printf("  Context         : %s\n", contextOrCurrent(kubeCtx))
	fmt.Printf("  Cluster name    : %s\n", clusterName)
	fmt.Printf("  Region          : %s\n", region)
	fmt.Printf("  Role ARN        : %s\n", roleARN)
	if intQueue != "" {
		fmt.Printf("  Interruption Q  : %s\n", intQueue)
	}
	fmt.Printf("  Karpenter       : %s\n", karpVer)
	fmt.Println()

	if !confirmPrompt("  Proceed with installation? [y/N] ") {
		fmt.Printf("  Cancelled.\n\n")
		return nil
	}

	fmt.Printf("\n  Installing Karpenter %s on AWS EKS…\n", karpVer)
	fmt.Printf("  (helm install wiring coming in next release)\n\n")
	return nil
}

// ── Azure AKS install flow ────────────────────────────────────────────────────

func runInstallAzure(kubeCtx, karpVer string) error {
	meta := kube.ProviderAzure.Meta()
	fmt.Println()
	printSection("Step 3: Azure AKS — Karpenter (Preview)")
	fmt.Printf(`
  Karpenter on Azure AKS uses a Microsoft-maintained provider chart.

  Chart   : %s
  Repo    : %s
  Docs    : %s

  Required Azure permissions:
    • Contributor on the node resource group
    • AKS Cluster Admin role

`, meta.ChartRepo, meta.ProviderRepo, meta.DocsURL)

	k8sVer, _ := kube.GetServerVersion(kubeCtx)
	if k8sVer != "" {
		fmt.Printf("  Kubernetes version  : %s\n\n", k8sVer)
	}

	if karpVer == "" {
		karpVer = askIfEmpty("", "Karpenter version to install (e.g. 0.7.0)", "")
	}

	fmt.Println()
	fmt.Printf("  Sample install command:\n\n")
	fmt.Printf("    helm install karpenter %s \\\n", meta.ChartRepo)
	fmt.Printf("      --namespace karpenter --create-namespace \\\n")
	if karpVer != "" {
		fmt.Printf("      --version %s \\\n", strings.TrimPrefix(karpVer, "v"))
	}
	fmt.Printf("      --set controller.resources.requests.cpu=1 \\\n")
	fmt.Printf("      --set controller.resources.requests.memory=1Gi\n\n")
	fmt.Printf("  Full setup guide: %s\n\n", meta.DocsURL)

	if !confirmPrompt("  Copy the command above and run it manually? Acknowledged [y/N] ") {
		fmt.Printf("  Cancelled.\n\n")
	}
	return nil
}

// ── GCP GKE install flow ──────────────────────────────────────────────────────

func runInstallGCP(kubeCtx, karpVer string) error {
	meta := kube.ProviderGCP.Meta()
	fmt.Println()
	printSection("Step 3: GCP GKE — Karpenter (Experimental)")
	fmt.Printf(`
  ⚠  The GCP Karpenter provider is experimental and community-maintained.
     It is NOT recommended for production workloads.

  Chart   : %s
  Repo    : %s
  Docs    : %s

`, meta.ChartRepo, meta.ProviderRepo, meta.DocsURL)

	k8sVer, _ := kube.GetServerVersion(kubeCtx)
	if k8sVer != "" {
		fmt.Printf("  Kubernetes version  : %s\n\n", k8sVer)
	}

	if karpVer == "" {
		karpVer = askIfEmpty("", "Karpenter version to install (e.g. 0.3.0)", "")
	}

	fmt.Println()
	fmt.Printf("  Sample install command:\n\n")
	fmt.Printf("    helm install karpenter %s \\\n", meta.ChartRepo)
	fmt.Printf("      --namespace karpenter --create-namespace \\\n")
	if karpVer != "" {
		fmt.Printf("      --version %s\n\n", strings.TrimPrefix(karpVer, "v"))
	}
	fmt.Printf("  Full setup guide: %s\n\n", meta.DocsURL)

	confirmPrompt("  Acknowledged (experimental) [y/N] ")
	return nil
}

// ─────────────────────────────────────────────────────────────────────────────
// upgrade command
// ─────────────────────────────────────────────────────────────────────────────

func upgradeCmd() *cobra.Command {
	var kubeCtx, targetVer string
	var reuseVals bool
	cmd := &cobra.Command{
		Use:     "upgrade",
		Short:   "Upgrade Karpenter to a specific or latest compatible version",
		Example: "  karpx upgrade -c my-cluster\n  karpx upgrade -c my-cluster --version v1.3.0",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runUpgrade(kubeCtx, targetVer, reuseVals)
		},
	}
	cmd.Flags().StringVarP(&kubeCtx,  "context",      "c", "",   "kubeconfig context")
	cmd.Flags().StringVar(&targetVer, "version",           "",   "target Karpenter version (default: latest compatible)")
	cmd.Flags().BoolVar(&reuseVals,   "reuse-values",     true, "pass --reuse-values to helm upgrade")
	return cmd
}

func runUpgrade(kubeCtx, targetVer string, reuseVals bool) error {
	fmt.Printf("\n  ▲ karpx upgrade  context:%s\n\n", contextOrCurrent(kubeCtx))

	// ── Detect installed Karpenter ────────────────────────────────────────
	info, err := helm.DetectKarpenter(kubeCtx)
	if err != nil || !info.Installed {
		fmt.Printf("  ✗ Karpenter is not installed on this cluster.\n")
		fmt.Printf("    Run `karpx install` to install it.\n\n")
		return nil
	}
	fmt.Printf("  Installed version   : v%s\n", info.Version)

	// ── Get Kubernetes version ────────────────────────────────────────────
	k8sVer, err := kube.GetServerVersion(kubeCtx)
	if err != nil {
		fmt.Printf("  ✗ Could not get cluster Kubernetes version: %v\n\n", err)
		return err
	}
	fmt.Printf("  Kubernetes version  : %s\n", k8sVer)

	// ── Resolve target version ────────────────────────────────────────────
	if targetVer == "" {
		fmt.Printf("\n  Fetching latest compatible version from GitHub…\n")
		latest, all, err := compat.LatestCompatible(k8sVer)
		if err != nil {
			fmt.Printf("  ✗ Could not fetch latest version: %v\n\n", err)
			return err
		}
		if latest == "" {
			fmt.Printf("  ✗ No compatible Karpenter version found for Kubernetes %s.\n\n", k8sVer)
			return nil
		}
		fmt.Printf("  Latest compatible   : v%s\n", latest)
		if len(all) > 1 {
			fmt.Printf("  All compatible      : %s\n", formatVersionList(all, 5))
		}
		targetVer = "v" + latest
	}

	installed := strings.TrimPrefix(info.Version, "v")
	target    := strings.TrimPrefix(targetVer, "v")

	if installed == target {
		fmt.Printf("\n  ✓  Already on %s — nothing to do.\n\n", targetVer)
		return nil
	}

	if !compat.IsCompatible(target, k8sVer) {
		fmt.Printf("\n  ✗ %s is NOT compatible with Kubernetes %s.\n", targetVer, k8sVer)
		fmt.Printf("    Choose a version from the compatible list above.\n\n")
		return fmt.Errorf("version %s incompatible with k8s %s", targetVer, k8sVer)
	}

	fmt.Printf("\n  Upgrade v%s → %s  (reuse-values: %v)\n", installed, targetVer, reuseVals)
	if !confirmPrompt("  Proceed with upgrade? [y/N] ") {
		fmt.Printf("  Cancelled.\n\n")
		return nil
	}

	fmt.Printf("\n  Upgrading Karpenter to %s…\n", targetVer)
	fmt.Printf("  (helm upgrade wiring coming in next release)\n\n")
	return nil
}

// ─────────────────────────────────────────────────────────────────────────────
// nodepools / version
// ─────────────────────────────────────────────────────────────────────────────

func nodePoolsCmd() *cobra.Command {
	var kubeCtx string
	cmd := &cobra.Command{
		Use:     "nodepools",
		Aliases: []string{"np"},
		Short:   "List NodePools and EC2NodeClasses on a cluster",
		Example: "  karpx nodepools -c my-cluster\n  karpx np -c my-cluster",
		RunE: func(cmd *cobra.Command, args []string) error {
			fmt.Printf("  karpx nodepools  context:%s\n", contextOrCurrent(kubeCtx))
			return nil
		},
	}
	cmd.Flags().StringVarP(&kubeCtx, "context", "c", "", "kubeconfig context")
	return cmd
}

func versionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print karpx version",
		Run: func(cmd *cobra.Command, args []string) {
			fmt.Printf("  karpx %s\n", version)
		},
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Interactive helpers
// ─────────────────────────────────────────────────────────────────────────────

// askProviderMenu shows a numbered menu and returns the chosen provider.
func askProviderMenu() kube.Provider {
	fmt.Printf(`
  Which cloud provider is this cluster running on?

    [1]  AWS EKS        — Karpenter fully supported  (production ready)
    [2]  Azure AKS      — Preview  (karpenter-provider-azure-aks)
    [3]  GCP GKE        — Experimental  (karpenter-provider-gcp)
    [4]  On-prem/Other  — No official Karpenter provider

`)
	fmt.Print("  Choice [1-4]: ")
	scanner := bufio.NewScanner(os.Stdin)
	if scanner.Scan() {
		switch strings.TrimSpace(scanner.Text()) {
		case "1":
			return kube.ProviderAWS
		case "2":
			return kube.ProviderAzure
		case "3":
			return kube.ProviderGCP
		case "4":
			return kube.ProviderUnknown
		}
	}
	return kube.ProviderUnknown
}

// askIfEmpty prompts the user for a value only when v is empty.
func askIfEmpty(v, prompt, defaultVal string) string {
	if v != "" {
		return v
	}
	if defaultVal != "" {
		fmt.Printf("  %s [%s]: ", prompt, defaultVal)
	} else {
		fmt.Printf("  %s: ", prompt)
	}
	scanner := bufio.NewScanner(os.Stdin)
	if scanner.Scan() {
		answer := strings.TrimSpace(scanner.Text())
		if answer != "" {
			return answer
		}
	}
	return defaultVal
}

// confirmPrompt reads y/yes from stdin.
func confirmPrompt(prompt string) bool {
	fmt.Print(prompt)
	scanner := bufio.NewScanner(os.Stdin)
	if scanner.Scan() {
		answer := strings.TrimSpace(strings.ToLower(scanner.Text()))
		return answer == "y" || answer == "yes"
	}
	return false
}

// confirmDefaultPrompt reads Enter (default yes) or n/no.
func confirmDefaultPrompt(prompt string) bool {
	fmt.Print(prompt)
	scanner := bufio.NewScanner(os.Stdin)
	if scanner.Scan() {
		answer := strings.TrimSpace(strings.ToLower(scanner.Text()))
		return answer == "" || answer == "y" || answer == "yes"
	}
	return true
}

// printSection prints a styled section header.
func printSection(label string) {
	fmt.Printf("  ── %s %s\n", label, strings.Repeat("─", max(0, 60-len(label))))
}

// printUnsupportedProvider explains why on-prem/other clusters aren't supported.
func printUnsupportedProvider() {
	fmt.Printf(`
  ✗ Karpenter does not currently have an official provider for
    on-prem or non-cloud Kubernetes clusters.

  Karpenter requires cloud-provider APIs (EC2, Azure VMSS, GCE) to
  provision and deprovision nodes. It cannot manage bare-metal or
  virtualisation-only environments.

  Alternatives for node autoscaling on on-prem clusters:
    • Cluster Autoscaler   https://github.com/kubernetes/autoscaler
    • KEDA (workload-based) https://keda.sh
    • Escalator            https://github.com/atlassian/escalator

  If your cluster IS on a supported cloud but was not detected,
  re-run with the explicit flag:
    karpx install --provider aws   -c <context>
    karpx install --provider azure -c <context>
    karpx install --provider gcp   -c <context>

`)
}

// ─────────────────────────────────────────────────────────────────────────────
// Utilities
// ─────────────────────────────────────────────────────────────────────────────

func contextOrCurrent(ctx string) string {
	if ctx == "" {
		return "(current context)"
	}
	return ctx
}

func formatVersionList(versions []string, limit int) string {
	if len(versions) <= limit {
		return "v" + strings.Join(versions, ", v")
	}
	return "v" + strings.Join(versions[:limit], ", v") +
		fmt.Sprintf(", … (%d more)", len(versions)-limit)
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
