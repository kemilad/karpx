package tui

import (
	"encoding/json"
	"errors"
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
	Name        string
	Mode        string // karpx.io/generated-mode annotation
	Ready       bool
	NotReadyMsg string // condition message when not ready
	CPULim      string
	MemLim      string
}

type NodeClassEntry struct {
	Name        string
	Role        string // EC2NodeClass spec.role or AWSNodeTemplate spec.instanceProfile
	Ready       bool
	NotReadyMsg string
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

	rightSide := "NodePools / EC2NodeClasses"
	if m.kubeCtx != "" {
		clusterName := m.kubeCtx
		// Extract just the cluster name from EKS ARNs (arn:...:cluster/NAME).
		if idx := strings.LastIndex(clusterName, "/"); idx >= 0 && strings.Contains(clusterName, ":cluster/") {
			clusterName = clusterName[idx+1:]
		}
		rightSide = clusterName + "  |  NodePools"
	}
	headerText := "  ⚡ karpx" + strings.Repeat(" ", max(0, m.width-len(rightSide)-10)) + rightSide
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
			if !np.Ready && np.NotReadyMsg != "" {
				b.WriteString(StyleMuted.Render("    └ "+np.NotReadyMsg) + "\n")
			}
		}
	}

	b.WriteString("\n")

	// ── EC2NodeClasses ─────────────────────────────────────────────────────
	b.WriteString(SectionTitle(fmt.Sprintf("EC2NodeClasses (%d)", len(m.nodeClasses))) + "\n\n")

	if len(m.nodeClasses) == 0 {
		b.WriteString(StyleMuted.Render("  No EC2NodeClasses found.") + "\n")
	} else {
		colName := 24
		colRole := 36

		hdr := fmt.Sprintf("  %-*s  %-*s  %s",
			colName, StyleTableHeader.Render("NAME"),
			colRole, StyleTableHeader.Render("NODE ROLE"),
			         StyleTableHeader.Render("READY"),
		)
		b.WriteString(hdr + "\n")
		b.WriteString(StyleMuted.Render("  "+strings.Repeat("─", max(0, min(m.width-4, 70)))) + "\n")

		for _, nc := range m.nodeClasses {
			ready := StyleSuccess.Render("✓")
			if !nc.Ready {
				ready = StyleDanger.Render("✗")
			}
			role := nc.Role
			if role == "" {
				role = "—"
			}
			if len(role) > colRole {
				role = role[:colRole-1] + "…"
			}
			row := fmt.Sprintf("  %-*s  %-*s  %s", colName, nc.Name, colRole, role, ready)
			b.WriteString(StyleRowNormal.Render(row) + "\n")
			if !nc.Ready && nc.NotReadyMsg != "" {
				b.WriteString(StyleMuted.Render("    └ "+nc.NotReadyMsg) + "\n")
			}
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

		// ── NodePools (v1beta1, Karpenter ≥ v0.31) ────────────────────────
		npArgs := []string{"get", "nodepools.karpenter.sh", "-o", "json"}
		if kubeCtx != "" {
			npArgs = append(npArgs, "--context", kubeCtx)
		}
		out, err := exec.Command("kubectl", npArgs...).Output()
		if err == nil {
			msg.nodePools = parseNodePools(out)
		} else {
			// Propagate hard errors (not "CRD not found").
			var exitErr *exec.ExitError
			if errors.As(err, &exitErr) {
				errStr := strings.TrimSpace(string(exitErr.Stderr))
				if errStr != "" &&
					!strings.Contains(errStr, "no matches for kind") &&
					!strings.Contains(errStr, "the server doesn't have a resource type") {
					msg.err = errStr
					return msg
				}
			}
		}

		// ── Provisioners (v1alpha5, Karpenter < v0.31) — fallback ─────────
		// Try when no NodePools were found (older cluster format).
		if len(msg.nodePools) == 0 {
			provArgs := []string{"get", "provisioners.karpenter.sh", "-o", "json"}
			if kubeCtx != "" {
				provArgs = append(provArgs, "--context", kubeCtx)
			}
			if out2, err2 := exec.Command("kubectl", provArgs...).Output(); err2 == nil {
				msg.nodePools = parseProvisioners(out2)
			}
		}

		// ── EC2NodeClasses (v1beta1, Karpenter ≥ v0.31) ───────────────────
		ncArgs := []string{"get", "ec2nodeclasses.karpenter.k8s.aws", "-o", "json"}
		if kubeCtx != "" {
			ncArgs = append(ncArgs, "--context", kubeCtx)
		}
		if out3, err3 := exec.Command("kubectl", ncArgs...).Output(); err3 == nil {
			msg.nodeClasses = parseNodeClasses(out3)
		}

		// ── AWSNodeTemplates (v1alpha1, Karpenter < v0.31) — fallback ─────
		if len(msg.nodeClasses) == 0 {
			antArgs := []string{"get", "awsnodetemplates.karpenter.k8s.aws", "-o", "json"}
			if kubeCtx != "" {
				antArgs = append(antArgs, "--context", kubeCtx)
			}
			if out4, err4 := exec.Command("kubectl", antArgs...).Output(); err4 == nil {
				msg.nodeClasses = parseAWSNodeTemplates(out4)
			}
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

// k8sMeta covers NodePool (v1beta1) and EC2NodeClass (v1beta1).
type k8sMeta struct {
	Metadata struct {
		Name        string            `json:"name"`
		Annotations map[string]string `json:"annotations"`
	} `json:"metadata"`
	Spec struct {
		Limits         map[string]string `json:"limits"`          // NodePool v1beta1
		Role           string            `json:"role"`            // EC2NodeClass v1beta1
		InstanceProfile string           `json:"instanceProfile"` // AWSNodeTemplate v1alpha1
	} `json:"spec"`
	Status struct {
		Conditions []struct {
			Type    string `json:"type"`
			Status  string `json:"status"`
			Message string `json:"message"`
			Reason  string `json:"reason"`
		} `json:"conditions"`
	} `json:"status"`
}

// provisionerMeta covers Provisioner (v1alpha5) where limits are nested.
type provisionerMeta struct {
	Metadata struct {
		Name        string            `json:"name"`
		Annotations map[string]string `json:"annotations"`
	} `json:"metadata"`
	Spec struct {
		Limits struct {
			Resources map[string]string `json:"resources"`
		} `json:"limits"`
	} `json:"spec"`
	Status struct {
		Conditions []struct {
			Type    string `json:"type"`
			Status  string `json:"status"`
			Message string `json:"message"`
			Reason  string `json:"reason"`
		} `json:"conditions"`
	} `json:"status"`
}

func readyStatus(m k8sMeta) (bool, string) {
	for _, c := range m.Status.Conditions {
		if c.Type == "Ready" {
			if c.Status == "True" {
				return true, ""
			}
			msg := c.Reason
			if c.Message != "" {
				msg = c.Message
			}
			return false, msg
		}
	}
	return false, ""
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
		ready, notReadyMsg := readyStatus(m)
		e := NodePoolEntry{
			Name:        m.Metadata.Name,
			Mode:        m.Metadata.Annotations["karpx.io/generated-mode"],
			Ready:       ready,
			NotReadyMsg: notReadyMsg,
			CPULim:      m.Spec.Limits["cpu"],
			MemLim:      m.Spec.Limits["memory"],
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
		ready, notReadyMsg := readyStatus(m)
		role := m.Spec.Role
		if role == "" {
			role = m.Spec.InstanceProfile
		}
		out = append(out, NodeClassEntry{
			Name:        m.Metadata.Name,
			Role:        role,
			Ready:       ready,
			NotReadyMsg: notReadyMsg,
		})
	}
	return out
}

// parseProvisioners handles the old Karpenter v1alpha5 Provisioner resource.
func parseProvisioners(data []byte) []NodePoolEntry {
	var list k8sList
	if err := json.Unmarshal(data, &list); err != nil {
		return nil
	}
	var out []NodePoolEntry
	for _, raw := range list.Items {
		var m provisionerMeta
		if err := json.Unmarshal(raw, &m); err != nil {
			continue
		}
		// Provisioner conditions use "Ready" same as NodePool.
		ready := false
		notReadyMsg := ""
		for _, c := range m.Status.Conditions {
			if c.Type == "Ready" {
				ready = c.Status == "True"
				if !ready {
					notReadyMsg = c.Message
					if notReadyMsg == "" {
						notReadyMsg = c.Reason
					}
				}
				break
			}
		}
		e := NodePoolEntry{
			Name:        m.Metadata.Name,
			Mode:        m.Metadata.Annotations["karpx.io/generated-mode"],
			Ready:       ready,
			NotReadyMsg: notReadyMsg,
			CPULim:      m.Spec.Limits.Resources["cpu"],
			MemLim:      m.Spec.Limits.Resources["memory"],
		}
		out = append(out, e)
	}
	return out
}

// parseAWSNodeTemplates handles the old Karpenter v1alpha1 AWSNodeTemplate resource.
func parseAWSNodeTemplates(data []byte) []NodeClassEntry {
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
		ready, notReadyMsg := readyStatus(m)
		role := m.Spec.InstanceProfile
		if role == "" {
			role = m.Spec.Role
		}
		out = append(out, NodeClassEntry{
			Name:        m.Metadata.Name,
			Role:        role,
			Ready:       ready,
			NotReadyMsg: notReadyMsg,
		})
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
