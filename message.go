package termview

type (
	OutputMsg struct{ ID int }
	ClosedMsg struct {
		ID              int
		ProcessExitCode int
		ProcessError    error
	}
	TextSelectedMsg struct {
		// Only used when mouse motion mode is enabled. See tea.MouseMode.
		ID   int
		Text string
	}
)
