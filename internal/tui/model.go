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
	viewAddons
)

// Model is the root BubbleTea model; it owns navigation between views.
type Model struct {
	cfg        Config
	current    view
	dashboard  *DashboardModel
	nodepools  *NodePoolsModel
	addonsView *AddonsModel
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
type addonsDoneMsg struct{ err error }

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
			m.nodepools = NewNodePoolsModel(msg.KubeContext)
			m.current = viewNodePools
			return m, m.nodepools.Init()
		case NavAddons:
			m.addonsView = NewAddonsModel(msg.KubeContext)
			m.current = viewAddons
			return m, m.addonsView.Init()
		case NavAddonsInstall:
			return m, m.execAddonsInstall(msg.AddonID, msg.KubeContext)
		case NavAddonsUninstall:
			return m, m.execAddonsUninstall(msg.AddonID, msg.KubeContext)
		}
		return m, nil

	case installDoneMsg, upgradeDoneMsg:
		m.current = viewDashboard
		m.dashboard.loading = true
		return m, loadClusters(m.dashboard.kubeCtx)

	case addonsDoneMsg:
		m.current = viewAddons
		if m.addonsView != nil {
			m.addonsView.loading = true
			return m, m.addonsView.Init()
		}
	}

	// Delegate all other messages to the active view.
	switch m.current {
	case viewDashboard:
		updated, cmd := m.dashboard.Update(msg)
		m.dashboard = updated
		return m, cmd
	case viewNodePools:
		if m.nodepools != nil {
			updated, cmd := m.nodepools.Update(msg)
			m.nodepools = updated
			return m, cmd
		}
	case viewAddons:
		if m.addonsView != nil {
			updated, cmd := m.addonsView.Update(msg)
			m.addonsView = updated
			return m, cmd
		}
	}

	return m, nil
}

func (m *Model) View() string {
	switch m.current {
	case viewNodePools:
		if m.nodepools != nil {
			return m.nodepools.View()
		}
	case viewAddons:
		if m.addonsView != nil {
			return m.addonsView.View()
		}
	}
	return m.dashboard.View()
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

// execAddonsInstall suspends the TUI and runs `karpx addons install <id> -c <context>`.
// When the process exits the TUI resumes and the add-ons view refreshes.
func (m *Model) execAddonsInstall(addonID, kubeCtx string) tea.Cmd {
	exe, err := os.Executable()
	if err != nil {
		exe = os.Args[0]
	}
	args := []string{"addons", "install", addonID}
	if kubeCtx != "" {
		args = append(args, "-c", kubeCtx)
	}
	cmd := exec.Command(exe, args...)
	return tea.ExecProcess(cmd, func(err error) tea.Msg {
		return addonsDoneMsg{err: err}
	})
}

// execAddonsUninstall suspends the TUI and runs `karpx addons uninstall <id> -c <context>`.
func (m *Model) execAddonsUninstall(addonID, kubeCtx string) tea.Cmd {
	exe, err := os.Executable()
	if err != nil {
		exe = os.Args[0]
	}
	args := []string{"addons", "uninstall", addonID}
	if kubeCtx != "" {
		args = append(args, "-c", kubeCtx)
	}
	cmd := exec.Command(exe, args...)
	return tea.ExecProcess(cmd, func(err error) tea.Msg {
		return addonsDoneMsg{err: err}
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
