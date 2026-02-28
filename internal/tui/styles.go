package tui

import "github.com/charmbracelet/lipgloss"

// karpx colour palette — violet/cyan on dark terminal.
var (
	colPrimary   = lipgloss.Color("#7C3AED") // violet  — brand colour
	colPrimaryLt = lipgloss.Color("#A78BFA") // violet light
	colSuccess   = lipgloss.Color("#10B981") // emerald
	colWarning   = lipgloss.Color("#F59E0B") // amber
	colDanger    = lipgloss.Color("#EF4444") // red
	colMuted     = lipgloss.Color("#6B7280") // gray
	colHighlight = lipgloss.Color("#F3F4F6") // near-white
	colBorder    = lipgloss.Color("#374151") // dark gray
	colAccent    = lipgloss.Color("#06B6D4") // cyan
	colBg        = lipgloss.Color("#0F0F1A") // near-black
	colSubtle    = lipgloss.Color("#1E1B4B") // indigo-dark (selected row bg)
)

// ─────────────────────────────────────────────────────────────────────────────
// Typography
// ─────────────────────────────────────────────────────────────────────────────

var (
	StyleTitle = lipgloss.NewStyle().
			Bold(true).
			Foreground(colPrimary)

	StyleSubtitle = lipgloss.NewStyle().
			Foreground(colAccent).
			Italic(true)

	StyleBanner = lipgloss.NewStyle().
			Foreground(colPrimary).
			Bold(true)

	StyleHelp = lipgloss.NewStyle().
			Foreground(colMuted).
			PaddingTop(1)

	StyleSuccess = lipgloss.NewStyle().Foreground(colSuccess).Bold(true)
	StyleWarning = lipgloss.NewStyle().Foreground(colWarning).Bold(true)
	StyleDanger  = lipgloss.NewStyle().Foreground(colDanger).Bold(true)
	StyleMuted   = lipgloss.NewStyle().Foreground(colMuted)
	StyleAccent  = lipgloss.NewStyle().Foreground(colAccent).Bold(true)
	StyleNormal  = lipgloss.NewStyle().Foreground(colHighlight)
)

// ─────────────────────────────────────────────────────────────────────────────
// Layout
// ─────────────────────────────────────────────────────────────────────────────

var (
	StylePanel = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(colBorder).
			Padding(0, 1)

	StyleActivePanel = lipgloss.NewStyle().
				Border(lipgloss.RoundedBorder()).
				BorderForeground(colPrimary).
				Padding(0, 1)

	StyleStatusBar = lipgloss.NewStyle().
			Background(colBorder).
			Foreground(colHighlight).
			Padding(0, 1)

	StyleHeader = lipgloss.NewStyle().
			Background(colSubtle).
			Foreground(colHighlight).
			Bold(true).
			Padding(0, 2)
)

// ─────────────────────────────────────────────────────────────────────────────
// Table
// ─────────────────────────────────────────────────────────────────────────────

var (
	StyleTableHeader = lipgloss.NewStyle().
				Bold(true).
				Foreground(colAccent).
				Underline(true)

	StyleRowSelected = lipgloss.NewStyle().
				Background(colSubtle).
				Foreground(colHighlight).
				Bold(true)

	StyleRowNormal = lipgloss.NewStyle().
			Foreground(colHighlight)
)

// ─────────────────────────────────────────────────────────────────────────────
// Status badges
// ─────────────────────────────────────────────────────────────────────────────

func BadgeInstalled() string {
	return lipgloss.NewStyle().
		Background(colSuccess).Foreground(colBg).Bold(true).Padding(0, 1).
		Render("● INSTALLED")
}

func BadgeUpgradeAvailable(toVer string) string {
	label := "▲ UPGRADE"
	if toVer != "" {
		label += " → v" + toVer
	}
	return lipgloss.NewStyle().
		Background(colWarning).Foreground(colBg).Bold(true).Padding(0, 1).
		Render(label)
}

// BadgeIncompatible is shown when the installed Karpenter version is not
// compatible with the cluster's Kubernetes version.
func BadgeIncompatible(toVer string) string {
	label := "✗ INCOMPATIBLE"
	if toVer != "" {
		label += " → v" + toVer
	}
	return lipgloss.NewStyle().
		Background(colDanger).Foreground(colHighlight).Bold(true).Padding(0, 1).
		Render(label)
}

func BadgeNotInstalled() string {
	return lipgloss.NewStyle().
		Background(colDanger).Foreground(colHighlight).Bold(true).Padding(0, 1).
		Render("✗ NOT INSTALLED")
}

func BadgeChecking() string {
	return lipgloss.NewStyle().
		Background(colBorder).Foreground(colHighlight).Padding(0, 1).
		Render("… CHECKING")
}

func BadgeError() string {
	return lipgloss.NewStyle().
		Background(colDanger).Foreground(colHighlight).Bold(true).Padding(0, 1).
		Render("! ERROR")
}

// ─────────────────────────────────────────────────────────────────────────────
// Keyboard hint renderer
// ─────────────────────────────────────────────────────────────────────────────

func Key(key, label string) string {
	k := lipgloss.NewStyle().
		Background(colBorder).Foreground(colPrimaryLt).Bold(true).Padding(0, 1).
		Render(key)
	l := StyleMuted.Render(" " + label)
	return k + l
}

func KeyActive(key, label string) string {
	k := lipgloss.NewStyle().
		Background(colPrimary).Foreground(colHighlight).Bold(true).Padding(0, 1).
		Render(key)
	l := StyleNormal.Render(" " + label)
	return k + l
}

// ─────────────────────────────────────────────────────────────────────────────
// Section header helper
// ─────────────────────────────────────────────────────────────────────────────

func SectionTitle(label string) string {
	left  := StyleMuted.Render("  ── ")
	title := lipgloss.NewStyle().Foreground(colPrimaryLt).Bold(true).Render(label)
	right := StyleMuted.Render(" ──────────────────────────────")
	return left + title + right
}
