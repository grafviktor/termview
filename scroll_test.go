package termview

import (
	"fmt"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/vt"
)

func newTestModel(w, h int) Model {
	return Model{emu: vt.NewSafeEmulator(w, h), width: w, height: h}
}

func TestScrollRendering(t *testing.T) {
	const w, h = 20, 5
	m := newTestModel(w, h)

	for i := 1; i <= 12; i++ {
		fmt.Fprintf(m.emu, "line%d\r\n", i)
	}

	t.Logf("scrollback len: %d", m.ScrollbackLen())

	// Live screen: last 4 lines plus the empty row the cursor sits on.
	live := m.View()
	t.Logf("live view:\n%s", live)
	if !strings.Contains(live, "line12") {
		t.Errorf("live view missing newest line:\n%s", live)
	}
	if strings.Contains(live, "line1\n") {
		t.Errorf("live view should not contain oldest line:\n%s", live)
	}

	// Scroll up three lines.
	m.ScrollUp(3)
	if m.ScrollOffset() != 3 {
		t.Fatalf("offset = %d, want 3", m.ScrollOffset())
	}
	if m.AtBottom() {
		t.Error("AtBottom() = true after scrolling up")
	}

	scrolled := m.View()
	t.Logf("scrolled(3) view:\n%s", scrolled)
	if got := len(strings.Split(scrolled, "\n")); got != h {
		t.Errorf("scrolled view has %d lines, want %d", got, h)
	}
	// Scrollback holds line1..line8, the screen line9..line12, so an offset of
	// three starts the viewport at line6 and mixes in the first screen rows.
	if want := "line6\nline7\nline8\nline9\nline10"; scrolled != want {
		t.Errorf("scrolled view = %q, want %q", scrolled, want)
	}
	if strings.Contains(scrolled, "line12") {
		t.Errorf("scrolled view should have pushed line12 out:\n%s", scrolled)
	}

	// Scrolling to the top clamps at the oldest scrollback line.
	m.ScrollToTop()
	top := m.View()
	t.Logf("top view (offset %d):\n%s", m.ScrollOffset(), top)
	if m.ScrollOffset() != m.ScrollbackLen() {
		t.Errorf("top offset = %d, want %d", m.ScrollOffset(), m.ScrollbackLen())
	}
	if !strings.Contains(top, "line1\n") {
		t.Errorf("top view missing oldest line:\n%s", top)
	}
	if got := len(strings.Split(top, "\n")); got != h {
		t.Errorf("top view has %d lines, want %d", got, h)
	}

	// Over-scrolling down clamps back to the live screen.
	m.ScrollDown(1000)
	if !m.AtBottom() {
		t.Errorf("offset = %d, want 0 after scrolling down past the end", m.ScrollOffset())
	}
	if m.View() != live {
		t.Errorf("returning to the bottom did not restore the live view:\n%s", m.View())
	}
}

func TestScrollAnchorsOnNewOutput(t *testing.T) {
	m := newTestModel(20, 5)
	for i := 1; i <= 12; i++ {
		fmt.Fprintf(m.emu, "line%d\r\n", i)
	}

	m.ScrollUp(3)
	m.lastScrollbackLen = m.ScrollbackLen()
	before := m.View()

	// Simulate the PTY goroutine pushing two more lines into the scrollback.
	fmt.Fprint(m.emu, "new1\r\nnew2\r\n")
	growth := m.ScrollbackLen() - m.lastScrollbackLen

	m, _ = m.Update(OutputMsg{ID: m.id})

	if got := m.View(); got != before {
		t.Errorf("view drifted while scrolled up.\nbefore:\n%s\nafter:\n%s", before, got)
	}
	if m.ScrollOffset() != 3+growth {
		t.Errorf("offset = %d, want %d", m.ScrollOffset(), 3+growth)
	}
}

func TestKeyPressReturnsToBottom(t *testing.T) {
	m := newTestModel(20, 5)
	for i := 1; i <= 12; i++ {
		fmt.Fprintf(m.emu, "line%d\r\n", i)
	}

	m = m.Focus()
	m.ScrollUp(3)

	// SendKey/SendText write into the emulator's response pipe, which blocks
	// without a reader. In the real model terminalViewToPty drains it into the PTY.
	emu := m.emu
	go func() {
		buf := make([]byte, 64)
		for {
			if _, err := emu.Read(buf); err != nil {
				return
			}
		}
	}()

	m, _ = m.Update(tea.KeyPressMsg{Code: 'x', Text: "x"})
	if !m.AtBottom() {
		t.Errorf("offset = %d, want 0 after a key press", m.ScrollOffset())
	}
}

func TestShiftedPrintableKeyIsSent(t *testing.T) {
	m := newTestModel(20, 5).Focus()
	emu := m.emu
	got := make(chan string, 1)
	go func() {
		buf := make([]byte, 64)
		n, err := emu.Read(buf)
		if err != nil {
			got <- ""
			return
		}
		got <- string(buf[:n])
	}()

	// Kitty/enhanced keyboard protocol reports Shift+a as Code 'a' + Text "A".
	m, _ = m.Update(tea.KeyPressMsg{Code: 'a', Text: "A", Mod: tea.ModShift})

	if out := <-got; out != "A" {
		t.Fatalf("pty input = %q, want %q", out, "A")
	}
}

func TestPageKeysScroll(t *testing.T) {
	m := newTestModel(20, 5)
	for i := 1; i <= 12; i++ {
		fmt.Fprintf(m.emu, "line%d\r\n", i)
	}
	m = m.Focus()

	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyPgUp})
	if m.ScrollOffset() != m.Height() {
		t.Fatalf("offset = %d after pgup, want %d", m.ScrollOffset(), m.Height())
	}

	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyPgDown})
	if !m.AtBottom() {
		t.Errorf("offset = %d after pgdown, want 0", m.ScrollOffset())
	}

	m = m.Blur()
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyPgUp})
	if !m.AtBottom() {
		t.Errorf("blurred terminal scrolled to %d", m.ScrollOffset())
	}
}

func TestMouseWheelScrolls(t *testing.T) {
	m := newTestModel(20, 5)
	for i := 1; i <= 12; i++ {
		fmt.Fprintf(m.emu, "line%d\r\n", i)
	}
	m = m.Focus()

	m, _ = m.Update(tea.MouseWheelMsg{Button: tea.MouseWheelUp})
	if m.ScrollOffset() != mouseScrollStep {
		t.Fatalf("offset = %d after one wheel up, want %d", m.ScrollOffset(), mouseScrollStep)
	}

	m, _ = m.Update(tea.MouseWheelMsg{Button: tea.MouseWheelDown})
	if !m.AtBottom() {
		t.Errorf("offset = %d after wheeling back down, want 0", m.ScrollOffset())
	}

	// Shift+wheel is the common "scroll scrollback" gesture.
	m, _ = m.Update(tea.MouseWheelMsg{Button: tea.MouseWheelUp})
	if m.ScrollOffset() != mouseScrollStep {
		t.Fatalf("offset = %d after shift+wheel up, want %d", m.ScrollOffset(), mouseScrollStep)
	}

	// An unfocused terminal ignores the wheel.
	m = m.Blur()
	m, _ = m.Update(tea.MouseWheelMsg{Button: tea.MouseWheelUp})
	if m.ScrollOffset() != mouseScrollStep {
		t.Errorf("blurred terminal scrolled to %d", m.ScrollOffset())
	}
}

func TestStyledScrollbackLineKeepsColors(t *testing.T) {
	m := newTestModel(20, 5)
	fmt.Fprint(m.emu, "\x1b[31mred line\x1b[0m\r\n")
	for i := range 12 {
		fmt.Fprintf(m.emu, "line%d\r\n", i)
	}

	m.ScrollToTop()
	top := m.View()
	if !strings.Contains(top, "red line") {
		t.Fatalf("top view missing the styled line:\n%q", top)
	}
	if !strings.Contains(top, "\x1b[") {
		t.Errorf("styled scrollback line lost its attributes:\n%q", top)
	}
	t.Logf("top view: %q", top)
}
