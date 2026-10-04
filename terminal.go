package termview

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"sync/atomic"
	"time"

	tea "charm.land/bubbletea/v2"
	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/term"
	"github.com/charmbracelet/x/vt"
	"github.com/charmbracelet/x/xpty"
)

const (
	minHeight           = 5
	minWidth            = 5
	defaultHeight       = 24
	defaultWidth        = 80
	scrollTimerInterval = 100 * time.Millisecond
)

var lastID atomic.Int64

func nextID() int {
	return int(lastID.Add(1))
}

type session struct {
	showCursor    atomic.Bool
	mouseTracking atomic.Uint32 // bitmask of enabled DEC mouse modes
	closed        atomic.Bool
	exited        chan struct{}
	exitCode      int
	exitErr       error
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
	// Scrolling
	scrollbackSize        int
	scrollOffset          int
	lastScrollbackLen     int
	scrollTimerInProgress bool
	// Selection
	isSelecting                bool
	startX, startY, endX, endY int
	pointerX, pointerY         int
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
		EnableMode: func(mode ansi.Mode) {
			if bit := mouseTrackingBit(mode); bit != 0 {
				state.mouseTracking.Or(bit)
			}
		},
		DisableMode: func(mode ansi.Mode) {
			if bit := mouseTrackingBit(mode); bit != 0 {
				state.mouseTracking.And(^bit)
			}
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
	var cmd tea.Cmd
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.resize(msg.Width, msg.Height)
		return m, nil
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
		// Only works when mouse motion mode is enabled. See tea.MouseMode
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
	case selectionEdgeScrollMsg:
		m.scrollTimerInProgress = false

		// If the pointer is still at the scren edge, then scroll.
		if msg.currentPositionY == m.pointerY && m.isSelecting {
			if m.pointerY == 0 {
				m.scrollUp(mouseScrollStep)
			} else if m.pointerY == m.height-1 {
				m.scrollDown(mouseScrollStep)
			}

			m.extendSelectionToPointer()

			// If user still holds the mouse at the same edge of the screen.
			if m.mouseAtTheEdge(m.pointerY) {
				// Then continue to emit scroll events.
				cmd = m.scrollBeyondTheEdge(msg.currentPositionY)
			}
		}
	}

	return m, cmd
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
			m.extendSelectionToPointer()
			return m, nil
		case "shift+pgdown":
			m.scrollDown(m.height)
			m.extendSelectionToPointer()
			return m, nil
		case "shift+up":
			m.scrollUp(1)
			m.extendSelectionToPointer()
			return m, nil
		case "shift+down":
			m.scrollDown(1)
			m.extendSelectionToPointer()
			return m, nil
		case "pgup":
			if m.isSelecting {
				m.scrollUp(m.height)
				m.extendSelectionToPointer()
				return m, nil
			}
		case "pgdown":
			if m.isSelecting {
				m.scrollDown(m.height)
				m.extendSelectionToPointer()
				return m, nil
			}
		case "up":
			if m.isSelecting {
				m.scrollUp(1)
				m.extendSelectionToPointer()
				return m, nil
			}
		case "down":
			if m.isSelecting {
				m.scrollDown(1)
				m.extendSelectionToPointer()
				return m, nil
			}
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

func (m Model) bufferY(screenY int) int {
	if m.emu == nil {
		return screenY
	}
	return screenY + m.emu.ScrollbackLen() - m.scrollOffset
}

// extendSelectionToPointer moves the selection end to the cell under the
// last pointer position. Call after scrolling so the selection grows with
// the viewport (keyboard, wheel, or edge auto-scroll).
func (m *Model) extendSelectionToPointer() {
	if !m.isSelecting {
		return
	}
	m.endX, m.endY = m.pointerX, m.bufferY(m.pointerY)
}

// mouseTrackingBit maps DEC mouse-tracking modes to bits in session.mouseTracking.
// SendMouse only emits sequences when at least one of these modes is set.
func mouseTrackingBit(mode ansi.Mode) uint32 {
	switch mode {
	case ansi.ModeMouseX10:
		return 1 << 0
	case ansi.ModeMouseNormal:
		return 1 << 1
	case ansi.ModeMouseHighlight:
		return 1 << 2
	case ansi.ModeMouseButtonEvent:
		return 1 << 3
	case ansi.ModeMouseAnyEvent:
		return 1 << 4
	default:
		return 0
	}
}

// forwardAltScreenWheel sends the wheel to the child. When the child has enabled
// DEC mouse tracking, emit a mouse sequence; otherwise fall back to cursor keys
// (xterm-style) so pagers like less still scroll.
func (m Model) forwardAltScreenWheel(msg tea.MouseWheelMsg) {
	if m.state != nil && m.state.mouseTracking.Load() != 0 {
		m.emu.SendMouse(uv.MouseWheelEvent{
			X:      msg.X,
			Y:      msg.Y,
			Button: msg.Button,
			Mod:    msg.Mod,
		})
		return
	}

	code := uv.KeyDown
	if msg.Button == tea.MouseWheelUp {
		code = uv.KeyUp
	}
	for range mouseScrollStep {
		m.emu.SendKey(uv.KeyPressEvent{Code: code})
	}
}

func (m Model) handleMouseMsg(msg tea.MouseMsg) (Model, tea.Cmd) {
	if !m.Focused() {
		return m, nil
	}

	var cmd tea.Cmd
	switch msg := msg.(type) {
	case tea.MouseClickMsg:
		if msg.Button == tea.MouseLeft {
			m.pointerX, m.pointerY = msg.X, msg.Y
			m.startX, m.startY = msg.X, m.bufferY(msg.Y)
			m.endX, m.endY = msg.X, m.bufferY(msg.Y)
			m.isSelecting = true
		}
	case tea.MouseMotionMsg:
		if m.isSelecting && msg.Button == tea.MouseLeft {
			m.pointerX, m.pointerY = msg.X, msg.Y
			m.extendSelectionToPointer()

			if m.mouseAtTheEdge(m.pointerY) {
				cmd = m.scrollBeyondTheEdge(msg.Y)
			}
		}
	case tea.MouseReleaseMsg:
		if m.hasSelection() && msg.Button == tea.MouseLeft {
			m.pointerX, m.pointerY = msg.X, msg.Y
			m.endX, m.endY = msg.X, m.bufferY(msg.Y)
			cmd = func() tea.Msg {
				return TextSelectedMsg{ID: m.id, Text: m.selectedText()}
			}
		}

		m.scrollTimerInProgress = false
		m.isSelecting = false
	case tea.MouseWheelMsg:
		if m.emu != nil && m.emu.IsAltScreen() {
			m.forwardAltScreenWheel(msg)
			return m, nil
		}
		// Requires the Bubble Tea view to set MouseMode (e.g. CellMotion).
		// That also captures click/drag, so host text selection usually breaks.
		switch msg.Button {
		case tea.MouseWheelUp:
			m.scrollUp(mouseScrollStep)
		case tea.MouseWheelDown:
			m.scrollDown(mouseScrollStep)
		}

		if m.isSelecting {
			if msg.X != 0 || msg.Y != 0 {
				m.pointerX, m.pointerY = msg.X, msg.Y
			}
			m.extendSelectionToPointer()
		}
	}

	return m, cmd
}

func (m *Model) scrollBeyondTheEdge(positionY int) tea.Cmd {
	if m.scrollTimerInProgress {
		return nil
	}

	var cmd tea.Cmd

	if m.isSelecting {
		m.scrollTimerInProgress = true
		cmd = tea.Tick(scrollTimerInterval, func(time.Time) tea.Msg {
			return selectionEdgeScrollMsg{currentPositionY: positionY}
		})
	}

	return cmd
}

func (m *Model) mouseAtTheEdge(positionY int) bool {
	return positionY == 0 || positionY == m.height-1
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
