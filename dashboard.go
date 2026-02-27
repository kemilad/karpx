package views

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"k8s.io/client-go/tools/clientcmd"
)

// -----------------------------------------------------------------------
// Messages
// -----------------------------------------------------------------------

type clustersLoadedMsg []ClusterEntry
type clusterCheckedMsg ClusterEntry

// ClusterEntry holds the state for one cluster row.
type ClusterEntry struct {
	Name          string
	Context       string
	Region        string
	Installed     bool
	Checking      bool
	ChartVersion  string
	LatestVersion string
	UpgradeNeeded bool
	Error         string
}

// -----------------------------------------------------------------------
// Model
// -----------------------------------------------------------------------

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
			if c.Context == msg.Context {
				m.clusters[i] = ClusterEntry(msg)
				break
			}
		}

	case tea.KeyMsg:
		switch msg.String() {
		case "up", "k":
			if m.cursor > 0 { m.cursor-- }
		case "down", "j":
			if m.cursor < len(m.clusters)-1 { m.cursor++ }
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

	// ── Header bar ─────────────────────────────────────────────────────
	header := StyleHeader.Width(m.width).Render(
		"  ⚡ karpx" +
		strings.Repeat(" ", max(0, m.width-30)) +
		"Karpenter Manager",
	)
	b.WriteString(header + "\n\n")

	// ── Loading state ───────────────────────────────────────────────────
	if m.loading {
		b.WriteString(StyleMuted.Render("  Loading clusters from kubeconfig...") + "\n")
		return b.String()
	}

	// ── Empty state ─────────────────────────────────────────────────────
	if len(m.clusters) == 0 {
		b.WriteString("\n")
		b.WriteString(SectionTitle("No clusters found") + "\n\n")
		b.WriteString(StyleMuted.Render("  Make sure kubectl is configured with at least one context.") + "\n")
		b.WriteString(StyleMuted.Render("  Try: kubectl config get-contexts") + "\n")
		return b.String()
	}

	// ── Table ───────────────────────────────────────────────────────────
	b.WriteString(SectionTitle(fmt.Sprintf("Clusters (%d)", len(m.clusters))) + "\n\n")

	// Column widths.
	colCluster := 32
	colVer     := 12
	colLatest  := 12

	headerRow := fmt.Sprintf("  %-*s  %-*s  %-*s  %s",
		colCluster, StyleTableHeader.Render("CLUSTER / CONTEXT"),
		colVer,     StyleTableHeader.Render("VERSION"),
		colLatest,  StyleTableHeader.Render("LATEST"),
		            StyleTableHeader.Render("STATUS"),
	)
	b.WriteString(headerRow + "\n")
	b.WriteString(StyleMuted.Render("  " + strings.Repeat("─", min(m.width-4, 80))) + "\n")

	for i, c := range m.clusters {
		b.WriteString(m.renderRow(c, i == m.cursor, colCluster, colVer, colLatest) + "\n")
	}

	// ── Detail panel for selected cluster ──────────────────────────────
	if sel := m.selected(); sel != nil {
		b.WriteString("\n")
		b.WriteString(m.renderDetail(sel))
	}

	// ── Key hints ──────────────────────────────────────────────────────
	b.WriteString("\n")
	b.WriteString(m.renderHints())
	return b.String()
}

// -----------------------------------------------------------------------
// Render helpers
// -----------------------------------------------------------------------

func (m *DashboardModel) renderRow(c ClusterEntry, selected bool, colCluster, colVer, colLatest int) string {
	badge := m.statusBadge(c)
	ver   := dash(c.ChartVersion)
	latest := dash(c.LatestVersion)

	name := c.Name
	if len(name) > colCluster {
		name = name[:colCluster-1] + "…"
	}

	row := fmt.Sprintf("  %-*s  %-*s  %-*s  %s",
		colCluster, name,
		colVer, ver,
		colLatest, latest,
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
	case c.UpgradeNeeded:
		return BadgeUpgradeAvailable(c.LatestVersion)
	default:
		return BadgeInstalled()
	}
}

func (m *DashboardModel) renderDetail(c *ClusterEntry) string {
	var b strings.Builder
	b.WriteString(SectionTitle("Selected") + "\n")
	detail := StylePanel.Render(
		StyleAccent.Render("  context  ") + StyleNormal.Render(c.Context) + "\n" +
		StyleAccent.Render("  region   ") + StyleNormal.Render(dash(c.Region)) + "\n" +
		StyleAccent.Render("  version  ") + StyleNormal.Render(dash(c.ChartVersion)),
	)
	b.WriteString(detail + "\n")
	return b.String()
}

func (m *DashboardModel) renderHints() string {
	hints := []string{Key("↑↓", "move"), Key("r", "refresh")}
	if sel := m.selected(); sel != nil {
		if !sel.Installed {
			hints = append(hints, KeyActive("i", "install"))
		} else {
			if sel.UpgradeNeeded {
				hints = append(hints, KeyActive("u", "upgrade"))
			}
			hints = append(hints, Key("n", "nodepools"))
		}
	}
	hints = append(hints, Key("q", "quit"))
	return "  " + strings.Join(hints, "  ") + "\n"
}

// -----------------------------------------------------------------------
// Navigation
// -----------------------------------------------------------------------

func (m *DashboardModel) selected() *ClusterEntry {
	if len(m.clusters) == 0 || m.cursor >= len(m.clusters) { return nil }
	return &m.clusters[m.cursor]
}

func (m *DashboardModel) navInstall() tea.Cmd {
	s := m.selected(); if s == nil { return nil }
	return func() tea.Msg { return NavigateMsg{Target: NavInstall, KubeContext: s.Context, Region: m.region} }
}

func (m *DashboardModel) navUpgrade() tea.Cmd {
	s := m.selected(); if s == nil || !s.Installed { return nil }
	return func() tea.Msg {
		return NavigateMsg{Target: NavUpgrade, KubeContext: s.Context, CurrentVersion: s.ChartVersion, Region: m.region}
	}
}

func (m *DashboardModel) navNodePools() tea.Cmd {
	s := m.selected(); if s == nil || !s.Installed { return nil }
	return func() tea.Msg { return NavigateMsg{Target: NavNodePools, KubeContext: s.Context, Region: m.region} }
}

// -----------------------------------------------------------------------
// Async commands
// -----------------------------------------------------------------------

func loadClusters(preferCtx string) tea.Cmd {
	return func() tea.Msg {
		rules := clientcmd.NewDefaultClientConfigLoadingRules()
		cfg, err := rules.Load()
		if err != nil { return clustersLoadedMsg{} }
		var entries []ClusterEntry
		for name := range cfg.Contexts {
			if preferCtx != "" && name != preferCtx { continue }
			entries = append(entries, ClusterEntry{Name: name, Context: name, Checking: true})
		}
		return clustersLoadedMsg(entries)
	}
}

func checkCluster(c ClusterEntry) tea.Cmd {
	return func() tea.Msg {
		// TODO: wire to internal/helm Detect()
		c.Checking = false
		c.Installed = false
		return clusterCheckedMsg(c)
	}
}

// -----------------------------------------------------------------------
// Utilities
// -----------------------------------------------------------------------

func dash(s string) string {
	if s == "" { return "─" }
	return s
}

func max(a, b int) int { if a > b { return a }; return b }
func min(a, b int) int { if a < b { return a }; return b }
