package main

import (
	"log"

	"github.com/grafviktor/termview"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

type Manager struct {
	init            tea.Cmd
	terminals       []termview.Model
	focusedTerminal int
	width           int
	height          int
	verticalView    bool
}

func New(terminalsCount int) Manager {
	terminals := []termview.Model{}

	for i := range terminalsCount {
		tw, err := termview.New(
			termview.WithCommand("/bin/bash"),
			termview.WithInitialWidth(80),
			termview.WithInitialHeight(24),
		)
		if err != nil {
			log.Fatalf("failed to start terminal %d: %v", i, err)
		}

		// Focus the first termview.
		if i == 0 {
			tw = tw.Focus()
		}
		terminals = append(terminals, tw)
	}

	return Manager{
		focusedTerminal: 0,
		terminals:       terminals,
		verticalView:    false,
	}
}

func (m Manager) Init() tea.Cmd {
	cmds := make([]tea.Cmd, len(m.terminals))
	for i, tw := range m.terminals {
		cmds[i] = tw.Init()
	}

	return tea.Batch(cmds...)
}

func (m Manager) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyPressMsg:
		if msg.String() == "ctrl+q" {
			m.closeTerminals()
			return m, tea.Quit
		}
		if msg.String() == "ctrl+w" {
			m.terminals[m.focusedTerminal] = m.terminals[m.focusedTerminal].Blur()
			m.focusedTerminal = (m.focusedTerminal + 1) % len(m.terminals)
			m.terminals[m.focusedTerminal] = m.terminals[m.focusedTerminal].Focus()
			return m, nil
		}
		if msg.String() == "ctrl+n" {
			m.verticalView = !m.verticalView
			return m.redraw()
		}
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		return m.redraw()
	case termview.OutputMsg:
		for i, tw := range m.terminals {
			if tw.ID() == msg.ID {
				updated, cmd := tw.Update(msg)
				m.terminals[i] = updated
				return m, cmd
			}
		}
		return m, nil

	case termview.ClosedMsg:
		m.closeTerminals()
		return m, tea.Quit
	}

	for i, tw := range m.terminals {
		if tw.Focused() {
			updated, cmd := tw.Update(msg)
			m.terminals[i] = updated
			return m, cmd
		}
	}

	return m, nil
}

func (m Manager) View() tea.View {
	if m.terminals == nil {
		v := tea.NewView("failed to start terminal")
		v.AltScreen = true
		return v
	}

	var views []string
	var cursor *tea.Cursor

	for _, tw := range m.terminals {
		tv := lipgloss.NewStyle().
			Width(tw.Width()).
			Height(tw.Height()).
			Render(tw.View())

		if tw.Focused() {
			views = append(views, focusedStyle.Render(tv))
		} else {
			views = append(views, paneStyle.Render(tv))
		}

		if !tw.Focused() {
			continue
		}

		cursor = m.getCursor(tw)
	}

	var v tea.View
	if m.verticalView {
		v = tea.NewView(lipgloss.JoinVertical(lipgloss.Left, views...))
	} else {
		v = tea.NewView(lipgloss.JoinHorizontal(lipgloss.Top, views...))
	}

	v.AltScreen = true
	// Leave MouseMode off so the host keeps text selection. Wheel scroll needs
	// MouseModeCellMotion, which blocks native select in many terminals.
	v.Cursor = cursor
	v.MouseMode = tea.MouseModeAllMotion
	return v
}

func (m Manager) getCursor(terminal termview.Model) *tea.Cursor {
	c := terminal.Cursor()
	if c == nil {
		return nil
	}

	// The first pane is very simple, we return cursor position relative to the borders of the pane.
	if m.focusedTerminal == 0 {
		/*
		* Why 1? Because for the first pane we count only left border width:
		* ----------
		* | cursor | <- 1 border in front of the cursor
		* ----------
		 */
		c.X += 1
		c.Y += 1

		return c
	}

	// For other panes we should calculate borders widths taking into account all previous panes.
	borderWidth := 1
	for i := 0; i < m.focusedTerminal; i++ {
		/*
		* Why 2? For all subsequent panes we should additionally add width of left and right
		* borders of the previous panes:
		* ----------||---------
		* |         || cursor | <- three borders before the cursor
		* ----------||--------
		 */
		borderWidth += 2
	}

	/*
	* Once we calculated borders widths, we must also calculate previous panes widths.
	* ------------||---------
	* | 1 2 3 4 5 || cursor | <- a single pane before the cursor and its width is 5,
	* ------------||--------     therefore we must move cursor by 5 positions from the left.
	 */
	var prevPanesWidth int
	for i := 0; i < m.focusedTerminal; i++ {
		w, h := m.terminals[i].Width(), m.terminals[i].Height()
		if m.verticalView {
			prevPanesWidth += h
		} else {
			prevPanesWidth += w
		}
	}

	if m.verticalView {
		c.X += 1
		c.Y += prevPanesWidth + borderWidth
	} else {
		c.X += prevPanesWidth + borderWidth
		c.Y += 1
	}

	return c
}

func (m Manager) redraw() (tea.Model, tea.Cmd) {
	if m.width == 0 || m.height == 0 {
		return m, func() tea.Msg { return tea.RequestWindowSize() }
	}

	width, height := m.calcTerminalSize()
	cmds := make([]tea.Cmd, len(m.terminals))
	for i, tw := range m.terminals {
		updated, cmd := tw.Update(tea.WindowSizeMsg{
			Width:  width,
			Height: height,
		})
		m.terminals[i] = updated
		cmds[i] = cmd
	}
	return m, tea.Batch(cmds...)
}

func (m Manager) calcTerminalSize() (width, height int) {
	var w, h int
	borderSize := 2
	nTerminals := len(m.terminals)

	if m.verticalView {
		w = m.width
		h = m.height / nTerminals
	} else {
		w = m.width / nTerminals
		h = m.height
	}

	width = w - borderSize
	height = h - borderSize

	if width < 5 {
		width = 5
	}

	if height < 5 {
		height = 5
	}

	return width, height
}

func (m Manager) closeTerminals() {
	for i := range m.terminals {
		if m.terminals[i].Closed() {
			continue
		}
		m.terminals[i].Close()
	}
}
