// Package tui implements the interactive terminal UI for karpx.
package tui

import (
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
	viewInstall
	viewUpgrade
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
			m.current = viewInstall
		case NavUpgrade:
			m.current = viewUpgrade
		case NavNodePools:
			m.current = viewNodePools
		}
		return m, nil
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
	case viewInstall:
		return StyleActivePanel.Render(
			"\n  ⚡ Install — coming soon\n\n" +
				"  Press Esc to return to the dashboard.\n",
		)
	case viewUpgrade:
		return StyleActivePanel.Render(
			"\n  ▲ Upgrade — coming soon\n\n" +
				"  Press Esc to return to the dashboard.\n",
		)
	case viewNodePools:
		return StyleActivePanel.Render(
			"\n  NodePools / EC2NodeClasses — coming soon\n\n" +
				"  Press Esc to return to the dashboard.\n",
		)
	default:
		return m.dashboard.View()
	}
}
