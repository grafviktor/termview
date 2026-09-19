package termview

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"sync/atomic"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/term"
	"github.com/charmbracelet/x/vt"
	"github.com/charmbracelet/x/xpty"
)

const (
	minHeight     = 5
	minWidth      = 5
	defaultHeight = 24
	defaultWidth  = 80
)

var lastID atomic.Int64

func nextID() int {
	return int(lastID.Add(1))
}

type session struct {
	showCursor atomic.Bool
	closed     atomic.Bool
	exited     chan struct{}
	exitCode   int
	exitErr    error
}

// Model embeds a shell in Bubble Tea using x/vt for emulation and xpty for the pseudo-terminal.
type Model struct {
	pty   xpty.Pty
	cmd   *exec.Cmd
	emu   *vt.SafeEmulator
	state *session

	id            int
	width, height int
	command       string
	commandArgs   []string
	focus         bool
	stdErr        io.Writer
	// Scrollback.
	scrollbackSize    int
	scrollOffset      int
	lastScrollbackLen int
	// Selection
	isSelecting                bool
	startX, startY, endX, endY int
}

func New(opts ...Option) (Model, error) {
	m := Model{id: nextID()}

	for _, opt := range opts {
		opt(&m)
	}

	if m.width == 0 || m.height == 0 {
		w, h := m.getDefaultSize()
		m.width, m.height = w, h
	}
	if m.command == "" {
		m.command = getShellPath()
	}

	cmd := buildCommand(m.command, m.commandArgs...)
	if m.stdErr != nil {
		cmd.Stderr = m.stdErr
	}

	// Init example taken from https://github.com/charmbracelet/freeze/blob/main/pty.go
	pty, err := xpty.NewPty(m.width, m.height)
	if err != nil {
		return m, err
	}
	if err := pty.Start(cmd); err != nil {
		return m, err
	}
	if up, ok := pty.(*xpty.UnixPty); ok {
		_ = up.Slave().Close()
	}

	emu := vt.NewSafeEmulator(m.width, m.height)
	if m.scrollbackSize > 0 {
		emu.SetScrollbackSize(m.scrollbackSize)
	}

	state := &session{exited: make(chan struct{})}
	state.showCursor.Store(true)
	emu.SetCallbacks(vt.Callbacks{
		CursorVisibility: func(visible bool) {
			state.showCursor.Store(visible)
		},
	})

	m.pty = pty
	m.cmd = cmd
	m.emu = emu
	m.state = state

	go m.waitProcessExit()

	return m, nil
}

// This is required for Windows. In Unix pty will be closed automatically when the process exits.
// And m.pty.Read(buf) will return EIO error once the PTY is closed, so the read loop will exit.
// On Windows, ConPTY does not return any errors on m.pty.Read(buf) and the reading loop just blocks.
// Here we monitor the process in a separate goroutine and let the app know that the process
// exited by sending a message through channel and closing the PTY.
func (m Model) waitProcessExit() {
	err := xpty.WaitProcess(context.Background(), m.cmd)

	exitCode := 0
	var exitErr *exec.ExitError
	switch {
	case errors.As(err, &exitErr):
		exitCode = exitErr.ExitCode()
	case m.cmd.ProcessState != nil:
		exitCode = m.cmd.ProcessState.ExitCode()
	}

	m.state.exitCode = exitCode
	m.state.exitErr = err
	close(m.state.exited)

	if m.pty != nil {
		_ = m.pty.Close()
	}
}

func (m Model) Init() tea.Cmd {
	// Forward anything the emulator writes to its response pipe — terminal
	// query replies *and* key bytes from SendKey — into the shell PTY.
	go m.terminalViewToPty()

	return m.ptyToTerminalView()
}

func (m Model) terminalViewToPty() {
	buf := make([]byte, 1024)
	for {
		if m.Closed() {
			return
		}

		n, err := m.emu.Read(buf)
		if n > 0 {
			_, _ = m.pty.Write(buf[:n])
		}
		if err != nil {
			return
		}
	}
}

func (m Model) ptyToTerminalView() tea.Cmd {
	return func() tea.Msg {
		buf := make([]byte, 4096)
		n, err := m.pty.Read(buf)

		if n > 0 {
			_, _ = m.emu.Write(buf[:n])
			return OutputMsg{ID: m.id}
		}
		if err != nil {
			// If there was an error we block and wait the process to exit.
			<-m.state.exited
			return ClosedMsg{
				ID:              m.id,
				ProcessExitCode: m.state.exitCode,
				ProcessError:    m.state.exitErr,
			}
		}

		return OutputMsg{ID: m.id}
	}
}

func (m Model) Update(msg tea.Msg) (Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.resize(msg.Width, msg.Height)
		if m.Closed() {
			return m, nil
		}
		return m, m.ptyToTerminalView()
	case OutputMsg:
		if m.id != msg.ID {
			return m, nil
		}
		if m.Closed() {
			return m, nil
		}

		m.followScrollback()
		return m, m.ptyToTerminalView()
	case ClosedMsg:
		if m.id != msg.ID {
			return m, nil
		}

		m.markClosed()
		return m, nil
	case tea.KeyPressMsg:
		return m.handleKeyPressMsg(msg)
	case tea.MouseMsg:
		// Only works when mouse motion mode is enabled. See tea.MouseMode.
		return m.handleMouseMsg(msg)
	case tea.PasteMsg:
		if !m.Focused() {
			return m, nil
		}
		if m.Closed() {
			return m, nil
		}

		m.scrollTo(0)
		m.emu.Paste(msg.Content)
		return m, nil
	}

	return m, nil
}

func (m Model) handleKeyPressMsg(msg tea.KeyPressMsg) (Model, tea.Cmd) {
	if !m.Focused() {
		return m, nil
	}
	if m.Closed() {
		return m, nil
	}

	// Page keys scroll the viewport instead of going to the shell, except
	// on the alternate screen where pagers and editors need them.
	if m.emu != nil && !m.emu.IsAltScreen() {
		switch msg.String() {
		case "shift+pgup":
			m.scrollUp(m.height)
			return m, nil
		case "shift+pgdown":
			m.scrollDown(m.height)
			return m, nil
		case "shift+up":
			m.scrollUp(1)
			return m, nil
		case "shift+down":
			m.scrollDown(1)
			return m, nil
		}
	}

	// Typing returns to the live screen, like a regular terminal does.
	m.scrollTo(0)
	// Prefer Text for printable characters (including Shift/CapsLock).
	// x/vt SendKey only emits printable keys when Mod == 0, so Shift+a
	// (Code:'a', Text:"A", Mod:Shift) would otherwise produce nothing.
	if msg.Text != "" {
		m.emu.SendText(msg.Text)
	} else {
		m.emu.SendKey(vt.KeyPressEvent(msg))
	}
	return m, nil
}

func (m Model) handleMouseMsg(msg tea.MouseMsg) (Model, tea.Cmd) {
	if !m.Focused() {
		return m, nil
	}

	var cmd tea.Cmd
	switch msg := msg.(type) {
	case tea.MouseClickMsg:
		if msg.Button == tea.MouseLeft {
			m.startX, m.startY = msg.X, msg.Y
			m.endX, m.endY = msg.X, msg.Y
			m.isSelecting = true
		}
	case tea.MouseMotionMsg:
		if m.isSelecting && msg.Button == tea.MouseLeft {
			m.endX, m.endY = msg.X, msg.Y
		}
	case tea.MouseReleaseMsg:
		if m.hasSelection() && msg.Button == tea.MouseLeft {
			m.endX, m.endY = msg.X, msg.Y
			cmd = func() tea.Msg {
				return TextSelectedMsg{ID: m.id, Text: m.selectedText()}
			}
		}
		m.isSelecting = false
	case tea.MouseWheelMsg:
		// Requires the Bubble Tea view to set MouseMode (e.g. CellMotion).
		// That also captures click/drag, so host text selection usually breaks.
		switch msg.Button {
		case tea.MouseWheelUp:
			m.scrollUp(mouseScrollStep)
		case tea.MouseWheelDown:
			m.scrollDown(mouseScrollStep)
		}
	}

	return m, cmd
}

func (m *Model) SetWidth(width int) {
	m.width = width
	m.resize(width, m.height)
}

func (m *Model) SetHeight(height int) {
	m.height = height
	m.resize(m.width, height)
}

func (m Model) Width() int {
	return m.width
}

func (m Model) Height() int {
	return m.height
}

func (m *Model) resize(width, height int) {
	if m.emu == nil || m.pty == nil {
		return
	}
	if m.Closed() {
		return
	}
	if width < minWidth {
		width = minWidth
	}
	if height < minHeight {
		height = minHeight
	}

	m.width, m.height = width, height
	m.emu.Resize(width, height)
	_ = m.pty.Resize(width, height)
	m.followScrollback()
}

func (m Model) getDefaultSize() (width, height int) {
	var err error
	m.width, m.height, err = term.GetSize(os.Stdout.Fd())
	if err != nil {
		return defaultWidth, defaultHeight
	}
	return m.width, m.height
}

func (m Model) View() string {
	if m.emu == nil {
		return ""
	}
	if m.hasSelection() {
		return m.viewWithSelection()
	}
	if m.scrollOffset > 0 {
		return m.viewScrollback()
	}
	return m.emu.Render()
}

func (m Model) Focus() Model {
	m.focus = true
	return m
}

func (m Model) Blur() Model {
	m.focus = false
	return m
}

func (m Model) Focused() bool {
	return m.focus
}

func (m Model) Cursor() *tea.Cursor {
	if m.Closed() {
		return nil
	}
	if !m.Focused() {
		return nil
	}
	if m.state == nil || !m.state.showCursor.Load() {
		return nil
	}

	// Scrolling up pushes the live screen down and eventually off the viewport.
	pos := m.emu.CursorPosition()
	y := pos.Y + m.scrollOffset
	if y >= m.height {
		return nil
	}

	return tea.NewCursor(pos.X, y)
}

func (m Model) ID() int {
	return m.id
}

func (m Model) Command() string {
	return m.command
}

func (m Model) Args() []string {
	return append([]string(nil), m.commandArgs...)
}

func (m Model) PID() int {
	if m.cmd != nil && m.cmd.Process != nil {
		return m.cmd.Process.Pid
	}
	return 0
}

func (m *Model) Close() {
	if m.Closed() {
		return
	}
	if m.cmd != nil && m.cmd.Process != nil {
		_ = m.cmd.Process.Kill()
		if m.state != nil {
			<-m.state.exited
		}
	}

	m.markClosed()

	// Now get rid of the emulator, we're explicitly done with it and no
	// longer need its output.
	if m.emu != nil {
		_ = m.emu.Emulator.Close()
	}
}

func (m *Model) markClosed() {
	if m.Closed() {
		return
	}
	if m.pty != nil {
		_ = m.pty.Close()
	}
	if m.state != nil {
		m.state.closed.Store(true)
	}
}

func (m Model) Closed() bool {
	return m.state != nil && m.state.closed.Load()
}
