// Package tui implements the interactive terminal UI for karpx.
package tui

import (
	"os"
	"os/exec"

	tea "github.com/charmbracelet/bubbletea"
)

// Config holds the runtime configuration passed from the CLI to the TUI.
type Config struct {
	KubeContext string
	Region      string
}

// view is the active screen identifier.
type view int

const (
	viewDashboard view = iota
	viewNodePools
)

// Model is the root BubbleTea model; it owns navigation between views.
type Model struct {
	cfg       Config
	current   view
	dashboard *DashboardModel
}

// NewModel constructs the root model and wires up the initial dashboard view.
func NewModel(cfg Config) *Model {
	return &Model{
		cfg:       cfg,
		current:   viewDashboard,
		dashboard: NewDashboard(cfg.KubeContext, cfg.Region),
	}
}

func (m *Model) Init() tea.Cmd {
	return m.dashboard.Init()
}

type installDoneMsg struct{ err error }
type upgradeDoneMsg struct{ err error }

func (m *Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "q", "ctrl+c":
			return m, tea.Quit
		case "esc":
			if m.current != viewDashboard {
				m.current = viewDashboard
				return m, nil
			}
		}

	case NavigateMsg:
		switch msg.Target {
		case NavInstall:
			return m, m.execInstall(msg.KubeContext, msg.Region)
		case NavUpgrade:
			return m, m.execUpgrade(msg.KubeContext, msg.Region)
		case NavNodePools:
			m.current = viewNodePools
		}
		return m, nil

	case installDoneMsg, upgradeDoneMsg:
		m.current = viewDashboard
		m.dashboard.loading = true
		return m, loadClusters(m.dashboard.kubeCtx)
	}

	// Delegate all other messages to the active view.
	switch m.current {
	case viewDashboard:
		updated, cmd := m.dashboard.Update(msg)
		m.dashboard = updated
		return m, cmd
	}

	return m, nil
}

func (m *Model) View() string {
	switch m.current {
	case viewNodePools:
		return StyleActivePanel.Render(
			"\n  NodePools / EC2NodeClasses — coming soon\n\n" +
				"  Press Esc to return to the dashboard.\n",
		)
	default:
		return m.dashboard.View()
	}
}

// execInstall suspends the TUI and runs `karpx install -c <context>` interactively.
// When the install command exits the TUI resumes and the dashboard refreshes.
func (m *Model) execInstall(kubeCtx, region string) tea.Cmd {
	exe, err := os.Executable()
	if err != nil {
		exe = os.Args[0]
	}
	args := []string{"install"}
	if kubeCtx != "" {
		args = append(args, "-c", kubeCtx)
	}
	if region != "" {
		args = append(args, "-r", region)
	}
	cmd := exec.Command(exe, args...)
	return tea.ExecProcess(cmd, func(err error) tea.Msg {
		return installDoneMsg{err: err}
	})
}

// execUpgrade suspends the TUI and runs `karpx upgrade -c <context>` interactively.
// When the upgrade command exits the TUI resumes and the dashboard refreshes.
func (m *Model) execUpgrade(kubeCtx, region string) tea.Cmd {
	exe, err := os.Executable()
	if err != nil {
		exe = os.Args[0]
	}
	args := []string{"upgrade"}
	if kubeCtx != "" {
		args = append(args, "-c", kubeCtx)
	}
	if region != "" {
		args = append(args, "-r", region)
	}
	cmd := exec.Command(exe, args...)
	return tea.ExecProcess(cmd, func(err error) tea.Msg {
		return upgradeDoneMsg{err: err}
	})
}
