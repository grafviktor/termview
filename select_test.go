package termview

import (
	"fmt"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

func TestSelectedTextIncludesOffscreenLines(t *testing.T) {
	m := newTestModel(20, 5)
	for i := 1; i <= 12; i++ {
		fmt.Fprintf(m.emu, "line%d\r\n", i)
	}

	sbLen := m.emu.ScrollbackLen()
	m.isSelecting = true
	m.startX, m.startY = 0, 0
	m.endX, m.endY = 19, sbLen+m.height-1

	got := m.selectedText()
	if !strings.Contains(got, "line1") {
		t.Errorf("copy missing oldest scrollback line:\n%s", got)
	}
	if !strings.Contains(got, "line12") {
		t.Errorf("copy missing live-screen line:\n%s", got)
	}
}

func TestBufferCellRejectsRowsPastScreen(t *testing.T) {
	m := newTestModel(20, 5)
	fmt.Fprint(m.emu, "hello\r\n")

	sbLen := m.emu.ScrollbackLen()
	if cell := m.bufferCell(0, sbLen+m.height); cell != nil {
		t.Errorf("bufferCell past the live screen = %#v, want nil", cell)
	}
}

func TestWheelWithoutCoordsKeepsPointer(t *testing.T) {
	m := newTestModel(20, 5)
	for i := 1; i <= 12; i++ {
		fmt.Fprintf(m.emu, "line%d\r\n", i)
	}
	m = m.Focus()

	m, _ = m.Update(tea.MouseClickMsg{Button: tea.MouseLeft, X: 4, Y: 3})
	m, _ = m.Update(tea.MouseMotionMsg{Button: tea.MouseLeft, X: 4, Y: 3})

	m, _ = m.Update(tea.MouseWheelMsg{Button: tea.MouseWheelUp})
	wantY := m.bufferY(3)
	if m.endX != 4 || m.endY != wantY {
		t.Errorf("end = (%d,%d), want (4,%d) from last pointer after scroll", m.endX, m.endY, wantY)
	}
}
