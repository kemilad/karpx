package tui

// NavTarget identifies which screen to navigate to.
type NavTarget int

const (
	NavInstall   NavTarget = iota
	NavUpgrade
	NavNodePools
)

// NavigateMsg is sent by child views to request a screen transition.
type NavigateMsg struct {
	Target         NavTarget
	KubeContext    string
	Region         string
	CurrentVersion string // set when navigating to NavUpgrade
}
