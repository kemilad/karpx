package main

import (
	"bufio"
	"fmt"
	"os"
	"runtime"
	"runtime/debug"
	"strings"

	"github.com/spf13/cobra"
	tea "github.com/charmbracelet/bubbletea"

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
		Short: "Karpenter manager for EKS — in your terminal",
		Long: banner + `  ⚡ Karpenter for EKS — managed from your terminal

  Open the interactive TUI to install, upgrade, and configure
  Karpenter across all your EKS clusters.

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
		Short:   "Check Karpenter installation and Kubernetes compatibility",
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

		compatible := compat.IsCompatible(info.Version, k8sVer)
		if compatible {
			fmt.Printf("  Compatibility       : ✓  compatible with Kubernetes %s\n", k8sVer)
		} else {
			fmt.Printf("  Compatibility       : ✗  NOT compatible with Kubernetes %s\n", k8sVer)
		}
	}

	// ── Latest compatible version from GitHub ─────────────────────────────
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

	// Show upgrade recommendation when needed.
	installed := strings.TrimPrefix(info.Version, "v")
	if !compat.IsCompatible(installed, k8sVer) {
		fmt.Printf("\n  ✗ Installed Karpenter is incompatible with this cluster.\n")
		fmt.Printf("  ► Run to upgrade:\n")
		fmt.Printf("    karpx upgrade -c %s --version v%s\n\n", contextOrCurrent(kubeCtx), latest)
	} else if installed != latest {
		fmt.Printf("\n  ▲ Upgrade available: v%s → v%s\n", installed, latest)
		fmt.Printf("  ► Run to upgrade:\n")
		fmt.Printf("    karpx upgrade -c %s --version v%s\n\n", contextOrCurrent(kubeCtx), latest)
	} else {
		fmt.Printf("\n  ✓  Karpenter is up to date.\n\n")
	}

	return nil
}

// ─────────────────────────────────────────────────────────────────────────────
// install command
// ─────────────────────────────────────────────────────────────────────────────

func installCmd() *cobra.Command {
	var kubeCtx, clusterName, region, roleARN, karpVer, intQueue string
	cmd := &cobra.Command{
		Use:   "install",
		Short: "Install Karpenter on an EKS cluster (non-interactive)",
		Example: `  karpx install \
    -c my-cluster \
    --cluster-name my-cluster \
    -r ap-southeast-1 \
    --role-arn arn:aws:iam::123456789:role/KarpenterController`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runInstall(kubeCtx, clusterName, region, roleARN, karpVer, intQueue)
		},
	}
	cmd.Flags().StringVarP(&kubeCtx,     "context",            "c", "",       "kubeconfig context")
	cmd.Flags().StringVarP(&clusterName, "cluster-name",       "n", "",       "EKS cluster name")
	cmd.Flags().StringVarP(&region,      "region",             "r", "",       "AWS region")
	cmd.Flags().StringVar(&roleARN,      "role-arn",               "",       "Karpenter controller IAM role ARN")
	cmd.Flags().StringVar(&karpVer,      "version",                "",       "Karpenter chart version (default: latest compatible)")
	cmd.Flags().StringVar(&intQueue,     "interruption-queue",     "",       "SQS queue for spot interruption")
	_ = cmd.MarkFlagRequired("cluster-name")
	_ = cmd.MarkFlagRequired("role-arn")
	return cmd
}

func runInstall(kubeCtx, clusterName, region, roleARN, karpVer, intQueue string) error {
	fmt.Printf("\n  ⚡ karpx install  context:%s  cluster:%s\n\n",
		contextOrCurrent(kubeCtx), clusterName)

	// ── Check if Karpenter is already installed ───────────────────────────
	fmt.Printf("  Checking existing installation…\n")
	info, _ := helm.DetectKarpenter(kubeCtx)
	if info.Installed {
		fmt.Printf("  ► Karpenter v%s is already installed in namespace %s.\n",
			info.Version, info.Namespace)

		// Check compatibility and offer upgrade path.
		k8sVer, err := kube.GetServerVersion(kubeCtx)
		if err == nil {
			if !compat.IsCompatible(info.Version, k8sVer) {
				fmt.Printf("  ✗ Installed version is NOT compatible with Kubernetes %s.\n", k8sVer)
				latest, _, _ := compat.LatestCompatible(k8sVer)
				if latest != "" {
					fmt.Printf("  ▲ Latest compatible: v%s\n\n", latest)
				}
				fmt.Printf("  Run `karpx upgrade` to upgrade instead of install.\n\n")
			} else {
				fmt.Printf("  ✓  Compatible with Kubernetes %s.\n", k8sVer)
				fmt.Printf("     Run `karpx upgrade` to upgrade to the latest version.\n\n")
			}
		}
		return nil
	}

	// ── Resolve target version ────────────────────────────────────────────
	if karpVer == "" {
		fmt.Printf("  Resolving latest compatible Karpenter version from GitHub…\n")
		k8sVer, err := kube.GetServerVersion(kubeCtx)
		if err != nil {
			fmt.Printf("  ✗ Could not get cluster version: %v\n", err)
			fmt.Printf("    Specify --version to override.\n\n")
			return err
		}
		latest, _, err := compat.LatestCompatible(k8sVer)
		if err != nil {
			fmt.Printf("  ✗ Could not fetch latest version: %v\n", err)
			fmt.Printf("    Specify --version to override.\n\n")
			return err
		}
		if latest == "" {
			fmt.Printf("  ✗ No compatible Karpenter version found for Kubernetes %s.\n\n", k8sVer)
			return fmt.Errorf("no compatible Karpenter version for k8s %s", k8sVer)
		}
		karpVer = "v" + latest
		fmt.Printf("  Selected version    : %s (compatible with Kubernetes %s)\n", karpVer, k8sVer)
	}

	// ── Proceed (Helm install stub — wire to Helm library in full impl) ───
	fmt.Printf("\n  Installing Karpenter %s…\n", karpVer)
	fmt.Printf("    cluster-name        : %s\n", clusterName)
	fmt.Printf("    region              : %s\n", region)
	fmt.Printf("    role-arn            : %s\n", roleARN)
	if intQueue != "" {
		fmt.Printf("    interruption-queue  : %s\n", intQueue)
	}
	fmt.Printf("\n  (helm install wiring coming in next release)\n\n")
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
	cmd.Flags().StringVarP(&kubeCtx,  "context",      "c", "",     "kubeconfig context")
	cmd.Flags().StringVar(&targetVer, "version",           "",     "target Karpenter version (default: latest compatible)")
	cmd.Flags().BoolVar(&reuseVals,   "reuse-values",     true,   "pass --reuse-values to helm upgrade")
	return cmd
}

func runUpgrade(kubeCtx, targetVer string, reuseVals bool) error {
	fmt.Printf("\n  ▲ karpx upgrade  context:%s\n\n", contextOrCurrent(kubeCtx))

	// ── Detect current install ────────────────────────────────────────────
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

	// ── Confirm or skip when already up to date ───────────────────────────
	installed := strings.TrimPrefix(info.Version, "v")
	target    := strings.TrimPrefix(targetVer, "v")
	if installed == target {
		fmt.Printf("\n  ✓  Already on %s — nothing to do.\n\n", targetVer)
		return nil
	}

	// Check the target version is actually compatible with k8s.
	if !compat.IsCompatible(target, k8sVer) {
		fmt.Printf("\n  ✗ %s is NOT compatible with Kubernetes %s.\n", targetVer, k8sVer)
		fmt.Printf("    Choose a version from the compatible list above.\n\n")
		return fmt.Errorf("version %s incompatible with k8s %s", targetVer, k8sVer)
	}

	// ── Confirm with user (interactive) ──────────────────────────────────
	fmt.Printf("\n  Upgrade v%s → %s  (reuse-values: %v)\n", installed, targetVer, reuseVals)
	if !confirmPrompt("  Proceed with upgrade? [y/N] ") {
		fmt.Printf("  Cancelled.\n\n")
		return nil
	}

	// ── Proceed (Helm upgrade stub) ───────────────────────────────────────
	fmt.Printf("\n  Upgrading Karpenter to %s…\n", targetVer)
	fmt.Printf("  (helm upgrade wiring coming in next release)\n\n")
	return nil
}

// ─────────────────────────────────────────────────────────────────────────────
// nodepools / version commands (unchanged)
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
// Utilities
// ─────────────────────────────────────────────────────────────────────────────

func contextOrCurrent(ctx string) string {
	if ctx == "" {
		return "(current context)"
	}
	return ctx
}

// confirmPrompt prints prompt and reads a y/Y response from stdin.
func confirmPrompt(prompt string) bool {
	fmt.Print(prompt)
	scanner := bufio.NewScanner(os.Stdin)
	if scanner.Scan() {
		answer := strings.TrimSpace(strings.ToLower(scanner.Text()))
		return answer == "y" || answer == "yes"
	}
	return false
}

// formatVersionList returns up to `limit` versions joined by ", " with "…" if truncated.
func formatVersionList(versions []string, limit int) string {
	if len(versions) <= limit {
		return "v" + strings.Join(versions, ", v")
	}
	return "v" + strings.Join(versions[:limit], ", v") + fmt.Sprintf(", … (%d more)", len(versions)-limit)
}
