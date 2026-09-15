package main

import (
	"log"
	"os"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/term"
	"github.com/grafviktor/termview"
)

type app struct {
	term termview.Model
}

func (a app) Init() tea.Cmd { return a.term.Init() }

func (a app) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case termview.OutputMsg:
		updated, cmd := a.term.Update(msg)
		a.term = updated
		return a, cmd
	case termview.ClosedMsg:
		return a, tea.Quit
	default:
		updated, cmd := a.term.Update(msg)
		a.term = updated
		return a, cmd
	}
}

func (a app) View() tea.View {
	v := tea.NewView(a.term.View())
	v.AltScreen = true
	v.MouseMode = tea.MouseModeCellMotion
	v.Cursor = a.term.Cursor()
	return v
}

func main() {
	if !term.IsTerminal(os.Stdout.Fd()) {
		log.Fatal("requires a real terminal")
	}

	t, err := termview.New(termview.WithCommand(os.Getenv("SHELL")))
	if err != nil {
		log.Fatal(err)
	}
	t = t.Focus()

	p := tea.NewProgram(app{term: t})
	if _, err := p.Run(); err != nil {
		log.Fatal(err)
	}
}
