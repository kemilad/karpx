package main

import (
	"fmt"
	"os"
	"runtime"
	"runtime/debug"

	"github.com/spf13/cobra"
	tea "github.com/charmbracelet/bubbletea"

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

func detectCmd() *cobra.Command {
	var kubeCtx string
	cmd := &cobra.Command{
		Use:     "detect",
		Short:   "Print the Karpenter version installed on a cluster",
		Example: "  karpx detect\n  karpx detect -c my-cluster",
		RunE: func(cmd *cobra.Command, args []string) error {
			fmt.Printf("  karpx detect  context:%s\n", contextOrCurrent(kubeCtx))
			return nil
		},
	}
	cmd.Flags().StringVarP(&kubeCtx, "context", "c", "", "kubeconfig context")
	return cmd
}

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
			fmt.Printf("  karpx install  context:%s  cluster:%s  region:%s\n",
				contextOrCurrent(kubeCtx), clusterName, region)
			fmt.Printf("  role-arn:%s  version:%s  queue:%s\n", roleARN, karpVer, intQueue)
			return nil
		},
	}
	cmd.Flags().StringVarP(&kubeCtx,     "context",            "c", "",       "kubeconfig context")
	cmd.Flags().StringVarP(&clusterName, "cluster-name",       "n", "",       "EKS cluster name")
	cmd.Flags().StringVarP(&region,      "region",             "r", "",       "AWS region")
	cmd.Flags().StringVar(&roleARN,      "role-arn",               "",       "Karpenter controller IAM role ARN")
	cmd.Flags().StringVar(&karpVer,      "version",                "latest", "Karpenter chart version")
	cmd.Flags().StringVar(&intQueue,     "interruption-queue",     "",       "SQS queue for spot interruption")
	_ = cmd.MarkFlagRequired("cluster-name")
	_ = cmd.MarkFlagRequired("role-arn")
	return cmd
}

func upgradeCmd() *cobra.Command {
	var kubeCtx, targetVer string
	var reuseVals bool
	cmd := &cobra.Command{
		Use:     "upgrade",
		Short:   "Upgrade Karpenter to a specific or latest compatible version",
		Example: "  karpx upgrade -c my-cluster\n  karpx upgrade -c my-cluster --version v1.3.0",
		RunE: func(cmd *cobra.Command, args []string) error {
			fmt.Printf("  karpx upgrade  context:%s  version:%s  reuse-values:%v\n",
				contextOrCurrent(kubeCtx), targetVer, reuseVals)
			return nil
		},
	}
	cmd.Flags().StringVarP(&kubeCtx,  "context",      "c", "",     "kubeconfig context")
	cmd.Flags().StringVar(&targetVer, "version",           "latest", "target Karpenter version")
	cmd.Flags().BoolVar(&reuseVals,   "reuse-values",     true,    "pass --reuse-values to helm upgrade")
	return cmd
}

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

func contextOrCurrent(ctx string) string {
	if ctx == "" { return "(current context)" }
	return ctx
}
