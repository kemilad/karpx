package tui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"k8s.io/client-go/tools/clientcmd"

	"github.com/Masterminds/semver/v3"
	"github.com/kemilad/karpx/internal/compat"
	"github.com/kemilad/karpx/internal/helm"
	"github.com/kemilad/karpx/internal/kube"
)

// ─────────────────────────────────────────────────────────────────────────────
// Messages
// ─────────────────────────────────────────────────────────────────────────────

type clustersLoadedMsg []ClusterEntry
type clusterCheckedMsg ClusterEntry

// ─────────────────────────────────────────────────────────────────────────────
// ClusterEntry
// ─────────────────────────────────────────────────────────────────────────────

// ClusterEntry holds per-cluster state rendered in the dashboard table.
type ClusterEntry struct {
	Name             string
	Context          string
	Region           string
	Provider         kube.Provider // detected cloud provider (aws / azure / gcp / unknown)
	K8sVersion       string        // cluster Kubernetes version, e.g. "1.30.2"
	Installed        bool
	Checking         bool
	ChartVersion     string // installed Karpenter version
	LatestVersion    string // latest compatible Karpenter version from GitHub
	UpgradeNeeded    bool   // true if installed version is incompatible OR newer exists
	Incompatible     bool   // true specifically when installed version is not compatible
	Error            string
}

// ─────────────────────────────────────────────────────────────────────────────
// Model
// ─────────────────────────────────────────────────────────────────────────────

type DashboardModel struct {
	clusters []ClusterEntry
	cursor   int
	loading  bool
	kubeCtx  string
	region   string
	width    int
	height   int
}

func NewDashboard(kubeCtx, region string) *DashboardModel {
	return &DashboardModel{kubeCtx: kubeCtx, region: region, loading: true}
}

func (m *DashboardModel) Init() tea.Cmd {
	return loadClusters(m.kubeCtx)
}

func (m *DashboardModel) Update(msg tea.Msg) (*DashboardModel, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height

	case clustersLoadedMsg:
		m.loading = false
		m.clusters = msg
		cmds := make([]tea.Cmd, len(m.clusters))
		for i := range m.clusters {
			cmds[i] = checkCluster(m.clusters[i])
		}
		return m, tea.Batch(cmds...)

	case clusterCheckedMsg:
		for i, c := range m.clusters {
			if c.Context == ClusterEntry(msg).Context {
				m.clusters[i] = ClusterEntry(msg)
				break
			}
		}

	case tea.KeyMsg:
		switch msg.String() {
		case "up", "k":
			if m.cursor > 0 {
				m.cursor--
			}
		case "down", "j":
			if m.cursor < len(m.clusters)-1 {
				m.cursor++
			}
		case "i":
			return m, m.navInstall()
		case "u":
			return m, m.navUpgrade()
		case "n":
			return m, m.navNodePools()
		case "r":
			m.loading = true
			return m, loadClusters(m.kubeCtx)
		}
	}
	return m, nil
}

func (m *DashboardModel) View() string {
	var b strings.Builder

	header := StyleHeader.Width(m.width).Render(
		"  ⚡ karpx" +
			strings.Repeat(" ", max(0, m.width-30)) +
			"Karpenter Manager",
	)
	b.WriteString(header + "\n\n")

	if m.loading {
		b.WriteString(StyleMuted.Render("  Loading clusters from kubeconfig...") + "\n")
		return b.String()
	}

	if len(m.clusters) == 0 {
		b.WriteString("\n")
		b.WriteString(SectionTitle("No clusters found") + "\n\n")
		b.WriteString(StyleMuted.Render("  Make sure kubectl is configured with at least one context.") + "\n")
		b.WriteString(StyleMuted.Render("  Try: kubectl config get-contexts") + "\n")
		return b.String()
	}

	b.WriteString(SectionTitle(fmt.Sprintf("Clusters (%d)", len(m.clusters))) + "\n\n")

	colCluster := 32
	colK8s     := 8
	colVer     := 12
	colLatest  := 12

	headerRow := fmt.Sprintf("  %-*s  %-*s  %-*s  %-*s  %s",
		colCluster, StyleTableHeader.Render("CLUSTER / CONTEXT"),
		colK8s,     StyleTableHeader.Render("K8S"),
		colVer,     StyleTableHeader.Render("KARPENTER"),
		colLatest,  StyleTableHeader.Render("LATEST"),
		            StyleTableHeader.Render("STATUS"),
	)
	b.WriteString(headerRow + "\n")
	b.WriteString(StyleMuted.Render("  "+strings.Repeat("─", min(m.width-4, 90))) + "\n")

	for i, c := range m.clusters {
		b.WriteString(m.renderRow(c, i == m.cursor, colCluster, colK8s, colVer, colLatest) + "\n")
	}

	if sel := m.selected(); sel != nil {
		b.WriteString("\n")
		b.WriteString(m.renderDetail(sel))
	}

	b.WriteString("\n")
	b.WriteString(m.renderHints())
	return b.String()
}

// ─────────────────────────────────────────────────────────────────────────────
// Render helpers
// ─────────────────────────────────────────────────────────────────────────────

func (m *DashboardModel) renderRow(c ClusterEntry, selected bool, colCluster, colK8s, colVer, colLatest int) string {
	badge  := m.statusBadge(c)
	k8sVer := dash(c.K8sVersion)
	ver    := dash(c.ChartVersion)
	latest := dash(c.LatestVersion)

	name := c.Name
	if len(name) > colCluster {
		name = name[:colCluster-1] + "…"
	}

	row := fmt.Sprintf("  %-*s  %-*s  %-*s  %-*s  %s",
		colCluster, name,
		colK8s,     k8sVer,
		colVer,     ver,
		colLatest,  latest,
		badge,
	)

	if selected {
		return lipgloss.NewStyle().
			Background(colSubtle).
			Foreground(colHighlight).
			Bold(true).
			Render(row)
	}
	return StyleRowNormal.Render(row)
}

func (m *DashboardModel) statusBadge(c ClusterEntry) string {
	switch {
	case c.Checking:
		return BadgeChecking()
	case c.Error != "":
		return BadgeError()
	case !c.Installed:
		return BadgeNotInstalled()
	case c.Incompatible:
		return BadgeIncompatible(c.LatestVersion)
	case c.UpgradeNeeded:
		return BadgeUpgradeAvailable(c.LatestVersion)
	default:
		return BadgeInstalled()
	}
}

func (m *DashboardModel) renderDetail(c *ClusterEntry) string {
	var b strings.Builder
	b.WriteString(SectionTitle("Selected") + "\n")

	meta := c.Provider.Meta()
	providerLabel := meta.Label
	if providerLabel == "" {
		providerLabel = "unknown"
	}
	providerLine := StyleNormal.Render(providerLabel)
	switch meta.SupportLevel {
	case "full":
		providerLine += "  " + StyleSuccess.Render("● full support")
	case "preview":
		providerLine += "  " + StyleWarning.Render("◐ preview")
	case "experimental":
		providerLine += "  " + StyleWarning.Render("◌ experimental")
	case "unsupported":
		providerLine += "  " + StyleDanger.Render("✗ no official Karpenter provider")
	}

	lines := StyleAccent.Render("  context    ") + StyleNormal.Render(c.Context) + "\n" +
		StyleAccent.Render("  provider   ") + providerLine + "\n" +
		StyleAccent.Render("  k8s        ") + StyleNormal.Render(dash(c.K8sVersion)) + "\n" +
		StyleAccent.Render("  karpenter  ") + StyleNormal.Render(dash(c.ChartVersion))

	if c.Provider == kube.ProviderUnknown {
		lines += "\n" + StyleMuted.Render("  ℹ  run `karpx install` for provider options and guidance")
	}
	if c.Incompatible {
		lines += "\n" + StyleDanger.Render("  ✗ installed version is NOT compatible with this Kubernetes version")
	}
	if c.UpgradeNeeded && c.LatestVersion != "" {
		lines += "\n" + StyleWarning.Render(fmt.Sprintf("  ▲ upgrade available → v%s", c.LatestVersion))
	}
	if !c.Installed && c.Provider != kube.ProviderUnknown && meta.DocsURL != "" {
		lines += "\n" + StyleMuted.Render("  docs: "+meta.DocsURL)
	}

	b.WriteString(StylePanel.Render(lines) + "\n")
	return b.String()
}

func (m *DashboardModel) renderHints() string {
	hints := []string{Key("↑↓", "move"), Key("r", "refresh")}
	if sel := m.selected(); sel != nil {
		if !sel.Installed {
			hints = append(hints, KeyActive("i", "install"))
		} else {
			if sel.UpgradeNeeded || sel.Incompatible {
				hints = append(hints, KeyActive("u", "upgrade"))
			}
			hints = append(hints, Key("n", "nodepools"))
		}
	}
	hints = append(hints, Key("q", "quit"))
	return "  " + strings.Join(hints, "  ") + "\n"
}

// ─────────────────────────────────────────────────────────────────────────────
// Navigation
// ─────────────────────────────────────────────────────────────────────────────

func (m *DashboardModel) selected() *ClusterEntry {
	if len(m.clusters) == 0 || m.cursor >= len(m.clusters) {
		return nil
	}
	return &m.clusters[m.cursor]
}

func (m *DashboardModel) navInstall() tea.Cmd {
	s := m.selected()
	if s == nil {
		return nil
	}
	return func() tea.Msg {
		return NavigateMsg{Target: NavInstall, KubeContext: s.Context, Region: m.region}
	}
}

func (m *DashboardModel) navUpgrade() tea.Cmd {
	s := m.selected()
	if s == nil || !s.Installed {
		return nil
	}
	return func() tea.Msg {
		return NavigateMsg{
			Target:         NavUpgrade,
			KubeContext:    s.Context,
			CurrentVersion: s.ChartVersion,
			Region:         m.region,
		}
	}
}

func (m *DashboardModel) navNodePools() tea.Cmd {
	s := m.selected()
	if s == nil || !s.Installed {
		return nil
	}
	return func() tea.Msg {
		return NavigateMsg{Target: NavNodePools, KubeContext: s.Context, Region: m.region}
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Async commands
// ─────────────────────────────────────────────────────────────────────────────

func loadClusters(preferCtx string) tea.Cmd {
	return func() tea.Msg {
		rules := clientcmd.NewDefaultClientConfigLoadingRules()
		cfg, err := rules.Load()
		if err != nil {
			return clustersLoadedMsg{}
		}
		var entries []ClusterEntry
		for name := range cfg.Contexts {
			if preferCtx != "" && name != preferCtx {
				continue
			}
			entries = append(entries, ClusterEntry{
				Name:     name,
				Context:  name,
				Checking: true,
			})
		}
		return clustersLoadedMsg(entries)
	}
}

// checkCluster is the core async command that:
//  1. Detects the cloud provider (AWS / Azure / GCP / unknown).
//  2. Detects whether Karpenter is installed (via helm).
//  3. Fetches the cluster's Kubernetes version.
//  4. Checks whether the installed Karpenter version is compatible.
//  5. Fetches the latest compatible Karpenter version from GitHub.
func checkCluster(c ClusterEntry) tea.Cmd {
	return func() tea.Msg {
		c.Checking = false

		// ── Step 1: detect cloud provider ──────────────────────────────────
		c.Provider = kube.DetectProvider(c.Context)

		// ── Step 2: detect Karpenter via helm ──────────────────────────────
		info, err := helm.DetectKarpenter(c.Context)
		if err != nil {
			c.Error = err.Error()
			return clusterCheckedMsg(c)
		}
		c.Installed = info.Installed
		if info.Installed {
			c.ChartVersion = info.Version
		}

		// ── Step 3: get cluster Kubernetes version ──────────────────────────
		k8sVer, err := kube.GetServerVersion(c.Context)
		if err != nil {
			c.Error = "k8s version: " + err.Error()
			return clusterCheckedMsg(c)
		}
		c.K8sVersion = k8sVer

		// ── Step 4: check compatibility (AWS provider only for now) ─────────
		if c.Installed && c.ChartVersion != "" && c.Provider == kube.ProviderAWS {
			c.Incompatible = !compat.IsCompatible(c.ChartVersion, k8sVer)
			if c.Incompatible {
				c.UpgradeNeeded = true
			}
		}

		// ── Step 5: fetch latest compatible version from GitHub ─────────────
		if c.Provider == kube.ProviderAWS {
			latest, _, err := compat.LatestCompatible(k8sVer)
			if err == nil && latest != "" {
				c.LatestVersion = latest
				if !c.UpgradeNeeded && c.Installed && c.ChartVersion != "" {
					iv, e1 := parseVer(c.ChartVersion)
					lv, e2 := parseVer(latest)
					if e1 == nil && e2 == nil && lv.GreaterThan(iv) {
						c.UpgradeNeeded = true
					}
				}
			}
		}

		return clusterCheckedMsg(c)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Utilities
// ─────────────────────────────────────────────────────────────────────────────

func parseVer(v string) (*semver.Version, error) {
	return semver.NewVersion(strings.TrimPrefix(v, "v"))
}

func dash(s string) string {
	if s == "" {
		return "─"
	}
	return s
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
