package termview

type (
	OutputMsg struct{ ID int }
	ClosedMsg struct {
		ID              int
		ProcessExitCode int
		ProcessError    error
	}
	// Only used when mouse motion mode is enabled. See tea.MouseMode.
	TextSelectedMsg struct {
		ID   int
		Text string
	}
)
