package tui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/kemilad/karpx/internal/addons"
)

// ─────────────────────────────────────────────────────────────────────────────
// Messages
// ─────────────────────────────────────────────────────────────────────────────

type addonsLoadedMsg []addons.Entry

// ─────────────────────────────────────────────────────────────────────────────
// Model
// ─────────────────────────────────────────────────────────────────────────────

type AddonsModel struct {
	kubeCtx string
	entries []addons.Entry
	cursor  int
	loading bool
	err     string
	width   int
	height  int
}

func NewAddonsModel(kubeCtx string) *AddonsModel {
	return &AddonsModel{kubeCtx: kubeCtx, loading: true}
}

func (m *AddonsModel) Init() tea.Cmd {
	return fetchAddons(m.kubeCtx)
}

func (m *AddonsModel) Update(msg tea.Msg) (*AddonsModel, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height

	case addonsLoadedMsg:
		m.loading = false
		m.entries = []addons.Entry(msg)

	case tea.KeyMsg:
		switch msg.String() {
		case "up", "k":
			if m.cursor > 0 {
				m.cursor--
			}
		case "down", "j":
			if m.cursor < len(m.entries)-1 {
				m.cursor++
			}
		case "i":
			return m, m.navInstall()
		case "x":
			return m, m.navUninstall()
		case "r":
			m.loading = true
			m.err = ""
			return m, fetchAddons(m.kubeCtx)
		}
	}
	return m, nil
}

func (m *AddonsModel) View() string {
	var b strings.Builder

	rightSide := "Add-ons"
	if m.kubeCtx != "" {
		clusterName := m.kubeCtx
		if idx := strings.LastIndex(clusterName, "/"); idx >= 0 && strings.Contains(clusterName, ":cluster/") {
			clusterName = clusterName[idx+1:]
		}
		rightSide = clusterName + "  |  Add-ons"
	}
	headerText := "  ⚡ karpx" + strings.Repeat(" ", max(0, m.width-len(rightSide)-10)) + rightSide
	header := StyleHeader.Width(max(1, m.width)).Render(headerText)
	b.WriteString(header + "\n\n")

	if m.loading {
		b.WriteString(StyleMuted.Render("  Checking add-on status…") + "\n")
		b.WriteString("\n  " + Key("esc", "back") + "  " + Key("q", "quit") + "\n")
		return b.String()
	}

	if m.err != "" {
		b.WriteString(StyleDanger.Render("  ✗ "+m.err) + "\n\n")
		b.WriteString("  " + Key("r", "retry") + "  " + Key("esc", "back") + "\n")
		return b.String()
	}

	b.WriteString(SectionTitle(fmt.Sprintf("Available Add-ons (%d)", len(m.entries))) + "\n\n")

	colName    := 22
	colCat     := 14
	colDesc    := 52
	colVer     := 10

	hdr := fmt.Sprintf("  %-*s  %-*s  %-*s  %-*s  %s",
		colName, StyleTableHeader.Render("NAME"),
		colCat,  StyleTableHeader.Render("CATEGORY"),
		colDesc, StyleTableHeader.Render("DESCRIPTION"),
		colVer,  StyleTableHeader.Render("VERSION"),
		         StyleTableHeader.Render("STATUS"),
	)
	b.WriteString(hdr + "\n")
	b.WriteString(StyleMuted.Render("  "+strings.Repeat("─", min(m.width-4, 116))) + "\n")

	for i, e := range m.entries {
		b.WriteString(m.renderRow(e, i == m.cursor, colName, colCat, colDesc, colVer) + "\n")
	}

	// Detail panel for selected entry
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

func (m *AddonsModel) renderRow(e addons.Entry, selected bool, colName, colCat, colDesc, colVer int) string {
	name := e.Name
	if len(name) > colName {
		name = name[:colName-1] + "…"
	}
	cat := e.Category
	desc := e.Description
	if len(desc) > colDesc {
		desc = desc[:colDesc-1] + "…"
	}
	ver := "─"
	if e.InstalledVersion != "" {
		ver = e.InstalledVersion
	}
	if len(ver) > colVer {
		ver = ver[:colVer-1] + "…"
	}
	badge := addonBadge(e)

	row := fmt.Sprintf("  %-*s  %-*s  %-*s  %-*s  %s",
		colName, name,
		colCat,  cat,
		colDesc, desc,
		colVer,  ver,
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

func (m *AddonsModel) renderDetail(e *addons.Entry) string {
	var b strings.Builder
	b.WriteString(SectionTitle("Selected") + "\n")

	lines := StyleAccent.Render("  addon       ") + StyleNormal.Render(e.Name) + "\n" +
		StyleAccent.Render("  category    ") + StyleNormal.Render(e.Category) + "\n" +
		StyleAccent.Render("  chart       ") + StyleNormal.Render(e.Chart) + "\n" +
		StyleAccent.Render("  namespace   ") + StyleNormal.Render(e.Namespace) + "\n" +
		StyleAccent.Render("  release     ") + StyleNormal.Render(e.Release)

	switch e.Status {
	case addons.StatusInstalled:
		lines += "\n" + StyleSuccess.Render("  ● installed")
		if e.InstalledVersion != "" {
			lines += StyleSuccess.Render("  version: "+e.InstalledVersion)
		}
	case addons.StatusNotInstalled:
		lines += "\n" + StyleMuted.Render("  ○ not installed  — press i to install")
	case addons.StatusError:
		lines += "\n" + StyleDanger.Render("  ✗ "+e.Error)
	}

	b.WriteString(StylePanel.Render(lines) + "\n")
	return b.String()
}

func (m *AddonsModel) renderHints() string {
	hints := []string{Key("↑↓", "move"), Key("r", "refresh")}
	if sel := m.selected(); sel != nil {
		if sel.Status == addons.StatusNotInstalled || sel.Status == addons.StatusUnknown {
			hints = append(hints, KeyActive("i", "install"))
		} else if sel.Status == addons.StatusInstalled {
			hints = append(hints, Key("x", "uninstall"))
		}
	}
	hints = append(hints, Key("esc", "back"), Key("q", "quit"))
	return "  " + strings.Join(hints, "  ") + "\n"
}

func addonBadge(e addons.Entry) string {
	switch e.Status {
	case addons.StatusInstalled:
		return lipgloss.NewStyle().
			Background(colSuccess).Foreground(colBg).Bold(true).Padding(0, 1).
			Render("● INSTALLED")
	case addons.StatusNotInstalled:
		return lipgloss.NewStyle().
			Background(colBorder).Foreground(colHighlight).Padding(0, 1).
			Render("○ NOT INSTALLED")
	case addons.StatusError:
		return lipgloss.NewStyle().
			Background(colDanger).Foreground(colHighlight).Bold(true).Padding(0, 1).
			Render("! ERROR")
	default:
		return lipgloss.NewStyle().
			Background(colBorder).Foreground(colHighlight).Padding(0, 1).
			Render("… CHECKING")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Navigation
// ─────────────────────────────────────────────────────────────────────────────

func (m *AddonsModel) selected() *addons.Entry {
	if len(m.entries) == 0 || m.cursor >= len(m.entries) {
		return nil
	}
	return &m.entries[m.cursor]
}

func (m *AddonsModel) navInstall() tea.Cmd {
	sel := m.selected()
	if sel == nil || sel.Status == addons.StatusInstalled {
		return nil
	}
	return func() tea.Msg {
		return NavigateMsg{Target: NavAddonsInstall, KubeContext: m.kubeCtx, AddonID: sel.ID}
	}
}

func (m *AddonsModel) navUninstall() tea.Cmd {
	sel := m.selected()
	if sel == nil || sel.Status != addons.StatusInstalled {
		return nil
	}
	return func() tea.Msg {
		return NavigateMsg{Target: NavAddonsUninstall, KubeContext: m.kubeCtx, AddonID: sel.ID}
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Async fetch
// ─────────────────────────────────────────────────────────────────────────────

func fetchAddons(kubeCtx string) tea.Cmd {
	return func() tea.Msg {
		catalog := addons.Registry()
		entries := make([]addons.Entry, len(catalog))
		for i, a := range catalog {
			entries[i] = addons.Detect(kubeCtx, a)
		}
		return addonsLoadedMsg(entries)
	}
}
