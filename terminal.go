package termview

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync/atomic"

	tea "charm.land/bubbletea/v2"
	uv "github.com/charmbracelet/ultraviolet"
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

	id                int
	width, height     int
	command           string
	commandArgs       []string
	focus             bool
	stdErr            io.Writer
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
			// If there was an error we block and wait the proess to exit.
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
			case "pgup", "shift+pgup":
				m.scrollUp(m.height)
				return m, nil
			case "pgdown", "shift+pgdown":
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
		var cmd tea.Cmd
		if m.hasSelection() && msg.Button == tea.MouseLeft {
			m.endX, m.endY = msg.X, msg.Y
			cmd = tea.SetClipboard(m.selectedText())
		}
		m.isSelecting = false
		return m, cmd
	case tea.MouseWheelMsg:
		if !m.Focused() {
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

		return m, nil

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

func (m Model) hasSelection() bool {
	if !m.isSelecting {
		return false
	}

	return m.startX != m.endX || m.startY != m.endY
}

// viewScrollback renders the viewport while scrolled up. The top rows come from
// the scrollback and the remaining ones from the top of the live screen.
func (m Model) viewScrollback() string {
	sbLen := m.emu.ScrollbackLen()
	// The shell can wipe the scrollback while we are scrolled up.
	start := sbLen - min(m.scrollOffset, sbLen)

	lines := make([]string, 0, m.height)
	for i := start; i < sbLen && len(lines) < m.height; i++ {
		lines = append(lines, m.scrollbackLine(i).Render())
	}

	for _, line := range strings.Split(m.emu.Render(), "\n") {
		if len(lines) >= m.height {
			break
		}
		lines = append(lines, line)
	}

	return strings.Join(lines, "\n")
}

func (m Model) viewWithSelection() string {
	x1, y1, x2, y2 := normalize(m.startX, m.startY, m.endX, m.endY)
	lines := make([]string, 0, m.height)
	for y := 0; y < m.height; y++ {
		line := make(uv.Line, 0, m.width)
		for x := 0; x < m.width; {
			cell := m.viewportCell(x, y)
			if cell == nil {
				line = append(line, uv.EmptyCell)
				x++
				continue
			}
			c := *cell
			if c.Width <= 0 {
				c.Width = 1
			}
			if inSelection(x, y, x1, y1, x2, y2) {
				c.Style.Attrs |= uv.AttrReverse
			}
			line = append(line, c)
			x += c.Width
		}
		lines = append(lines, line.Render())
	}
	return strings.Join(lines, "\n")
}

func (m Model) viewportCell(x, y int) *uv.Cell {
	if m.emu == nil || x < 0 || y < 0 || x >= m.width || y >= m.height {
		return nil
	}
	if m.scrollOffset == 0 {
		return m.emu.CellAt(x, y)
	}

	sbLen := m.emu.ScrollbackLen()
	start := sbLen - min(m.scrollOffset, sbLen)
	sbRows := sbLen - start
	if y < sbRows {
		return m.emu.ScrollbackCellAt(x, start+y)
	}
	return m.emu.CellAt(x, y-sbRows)
}

func normalize(sx, sy, ex, ey int) (x1, y1, x2, y2 int) {
	x1, y1, x2, y2 = sx, sy, ex, ey

	if y1 > y2 {
		return x2, y2, x1, y1
	}
	if y1 == y2 && x1 > x2 {
		return x2, y2, x1, y1
	}
	return x1, y1, x2, y2
}

func inSelection(x, y, x1, y1, x2, y2 int) bool {
	if y < y1 || y > y2 {
		return false
	}
	if y1 == y2 {
		return x >= x1 && x <= x2
	}
	if y == y1 {
		return x >= x1
	}
	if y == y2 {
		return x <= x2
	}
	return true // full middle lines
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

func (m Model) selectedText() string {
	x1, y1, x2, y2 := normalize(m.startX, m.startY, m.endX, m.endY)
	lines := []string{}
	for y := 0; y < m.height; y++ {
		selectedLine := false
		var str strings.Builder
		for x := 0; x < m.width; {
			cell := m.viewportCell(x, y)
			if cell == nil {
				// If the cell is nil, we've reached the end of the line.
				// That happems only when we search for cells in the scrollback buffer.
				break
			}
			if inSelection(x, y, x1, y1, x2, y2) {
				str.WriteString(cell.Content)
				selectedLine = true
			}
			x += cell.Width
		}
		if selectedLine {
			lines = append(lines, strings.TrimRight(str.String(), " "))
		}
	}
	return strings.Join(lines, "\n")
}
