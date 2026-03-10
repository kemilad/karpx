package tui

import (
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// ─────────────────────────────────────────────────────────────────────────────
// Data types
// ─────────────────────────────────────────────────────────────────────────────

type NodePoolEntry struct {
	Name   string
	Mode   string // karpx.io/generated-mode annotation
	Ready  bool
	CPULim string
	MemLim string
}

type NodeClassEntry struct {
	Name  string
	Ready bool
}

// ─────────────────────────────────────────────────────────────────────────────
// Messages
// ─────────────────────────────────────────────────────────────────────────────

type nodePoolsLoadedMsg struct {
	nodePools  []NodePoolEntry
	nodeClasses []NodeClassEntry
	err        string
}

// ─────────────────────────────────────────────────────────────────────────────
// Model
// ─────────────────────────────────────────────────────────────────────────────

type NodePoolsModel struct {
	kubeCtx     string
	nodePools   []NodePoolEntry
	nodeClasses []NodeClassEntry
	loading     bool
	err         string
	width       int
	height      int
}

func NewNodePoolsModel(kubeCtx string) *NodePoolsModel {
	return &NodePoolsModel{kubeCtx: kubeCtx, loading: true}
}

func (m *NodePoolsModel) Init() tea.Cmd {
	return fetchNodePools(m.kubeCtx)
}

func (m *NodePoolsModel) Update(msg tea.Msg) (*NodePoolsModel, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height

	case nodePoolsLoadedMsg:
		m.loading = false
		m.err = msg.err
		m.nodePools = msg.nodePools
		m.nodeClasses = msg.nodeClasses

	case tea.KeyMsg:
		switch msg.String() {
		case "r":
			m.loading = true
			m.err = ""
			return m, fetchNodePools(m.kubeCtx)
		}
	}
	return m, nil
}

func (m *NodePoolsModel) View() string {
	var b strings.Builder

	headerText := "  ⚡ karpx" + strings.Repeat(" ", max(0, m.width-36)) + "NodePools / EC2NodeClasses"
	header := StyleHeader.Width(max(1, m.width)).Render(headerText)
	b.WriteString(header + "\n\n")

	if m.loading {
		b.WriteString(StyleMuted.Render("  Fetching NodePools from cluster…") + "\n")
		b.WriteString("\n  " + Key("esc", "back") + "  " + Key("q", "quit") + "\n")
		return b.String()
	}

	if m.err != "" {
		b.WriteString(StyleDanger.Render("  ✗ "+m.err) + "\n\n")
		b.WriteString("  " + Key("r", "retry") + "  " + Key("esc", "back") + "\n")
		return b.String()
	}

	// ── NodePools ──────────────────────────────────────────────────────────
	b.WriteString(SectionTitle(fmt.Sprintf("NodePools (%d)", len(m.nodePools))) + "\n\n")

	if len(m.nodePools) == 0 {
		b.WriteString(StyleMuted.Render("  No NodePools found.") + "\n")
		b.WriteString(StyleMuted.Render("  Run `karpx nodes` or use the web UI to generate one.") + "\n")
	} else {
		colName := 24
		colMode := 14
		colLim  := 20

		hdr := fmt.Sprintf("  %-*s  %-*s  %-*s  %s",
			colName, StyleTableHeader.Render("NAME"),
			colMode, StyleTableHeader.Render("MODE"),
			colLim,  StyleTableHeader.Render("LIMITS (cpu / mem)"),
			         StyleTableHeader.Render("READY"),
		)
		b.WriteString(hdr + "\n")
		b.WriteString(StyleMuted.Render("  "+strings.Repeat("─", max(0, min(m.width-4, 80)))) + "\n")

		for _, np := range m.nodePools {
			ready := StyleSuccess.Render("✓")
			if !np.Ready {
				ready = StyleDanger.Render("✗")
			}
			lim := np.CPULim + " / " + np.MemLim
			if np.CPULim == "" && np.MemLim == "" {
				lim = "—"
			}
			mode := np.Mode
			if mode == "" {
				mode = "—"
			}
			row := fmt.Sprintf("  %-*s  %-*s  %-*s  %s",
				colName, np.Name,
				colMode, mode,
				colLim,  lim,
				         ready,
			)
			b.WriteString(StyleRowNormal.Render(row) + "\n")
		}
	}

	b.WriteString("\n")

	// ── EC2NodeClasses ─────────────────────────────────────────────────────
	b.WriteString(SectionTitle(fmt.Sprintf("EC2NodeClasses (%d)", len(m.nodeClasses))) + "\n\n")

	if len(m.nodeClasses) == 0 {
		b.WriteString(StyleMuted.Render("  No EC2NodeClasses found.") + "\n")
	} else {
		colName := 24

		hdr := fmt.Sprintf("  %-*s  %s",
			colName, StyleTableHeader.Render("NAME"),
			         StyleTableHeader.Render("READY"),
		)
		b.WriteString(hdr + "\n")
		b.WriteString(StyleMuted.Render("  "+strings.Repeat("─", min(m.width-4, 40))) + "\n")

		for _, nc := range m.nodeClasses {
			ready := StyleSuccess.Render("✓")
			if !nc.Ready {
				ready = StyleDanger.Render("✗")
			}
			row := fmt.Sprintf("  %-*s  %s", colName, nc.Name, ready)
			b.WriteString(StyleRowNormal.Render(row) + "\n")
		}
	}

	b.WriteString("\n")
	b.WriteString("  " + Key("r", "refresh") + "  " + Key("esc", "back") + "  " + Key("q", "quit") + "\n")
	return b.String()
}

// ─────────────────────────────────────────────────────────────────────────────
// Async fetch command
// ─────────────────────────────────────────────────────────────────────────────

func fetchNodePools(kubeCtx string) tea.Cmd {
	return func() tea.Msg {
		msg := nodePoolsLoadedMsg{}

		// ── NodePools ──────────────────────────────────────────────────────
		npArgs := []string{"get", "nodepools", "-o", "json"}
		if kubeCtx != "" {
			npArgs = append(npArgs, "--context", kubeCtx)
		}
		out, err := exec.Command("kubectl", npArgs...).Output()
		if err != nil {
			// CRDs may not be installed yet — treat as empty rather than error
			errStr := string(err.(*exec.ExitError).Stderr)
			if !strings.Contains(errStr, "no matches for kind") &&
				!strings.Contains(errStr, "the server doesn't have a resource type") {
				msg.err = strings.TrimSpace(errStr)
				return msg
			}
		} else {
			msg.nodePools = parseNodePools(out)
		}

		// ── EC2NodeClasses ─────────────────────────────────────────────────
		ncArgs := []string{"get", "ec2nodeclasses", "-o", "json"}
		if kubeCtx != "" {
			ncArgs = append(ncArgs, "--context", kubeCtx)
		}
		out2, err2 := exec.Command("kubectl", ncArgs...).Output()
		if err2 == nil {
			msg.nodeClasses = parseNodeClasses(out2)
		}

		return msg
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// JSON parsing helpers
// ─────────────────────────────────────────────────────────────────────────────

type k8sList struct {
	Items []json.RawMessage `json:"items"`
}

type k8sMeta struct {
	Metadata struct {
		Name        string            `json:"name"`
		Annotations map[string]string `json:"annotations"`
	} `json:"metadata"`
	Spec struct {
		Limits map[string]string `json:"limits"`
	} `json:"spec"`
	Status struct {
		Conditions []struct {
			Type   string `json:"type"`
			Status string `json:"status"`
		} `json:"conditions"`
	} `json:"status"`
}

func isReady(m k8sMeta) bool {
	for _, c := range m.Status.Conditions {
		if c.Type == "Ready" {
			return c.Status == "True"
		}
	}
	return false
}

func parseNodePools(data []byte) []NodePoolEntry {
	var list k8sList
	if err := json.Unmarshal(data, &list); err != nil {
		return nil
	}
	var out []NodePoolEntry
	for _, raw := range list.Items {
		var m k8sMeta
		if err := json.Unmarshal(raw, &m); err != nil {
			continue
		}
		e := NodePoolEntry{
			Name:   m.Metadata.Name,
			Mode:   m.Metadata.Annotations["karpx.io/generated-mode"],
			Ready:  isReady(m),
			CPULim: m.Spec.Limits["cpu"],
			MemLim: m.Spec.Limits["memory"],
		}
		out = append(out, e)
	}
	return out
}

func parseNodeClasses(data []byte) []NodeClassEntry {
	var list k8sList
	if err := json.Unmarshal(data, &list); err != nil {
		return nil
	}
	var out []NodeClassEntry
	for _, raw := range list.Items {
		var m k8sMeta
		if err := json.Unmarshal(raw, &m); err != nil {
			continue
		}
		out = append(out, NodeClassEntry{Name: m.Metadata.Name, Ready: isReady(m)})
	}
	return out
}

// badge helpers reused from styles
func badgeReady() string {
	return lipgloss.NewStyle().
		Background(colSuccess).Foreground(colBg).Bold(true).Padding(0, 1).
		Render("● READY")
}

func badgeNotReady() string {
	return lipgloss.NewStyle().
		Background(colDanger).Foreground(colHighlight).Bold(true).Padding(0, 1).
		Render("✗ NOT READY")
}
