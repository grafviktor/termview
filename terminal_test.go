package termview

import (
	"testing"

	tea "charm.land/bubbletea/v2"
)

func TestResizeDoesNotStartAnotherPtyRead(t *testing.T) {
	m := newTestModel(20, 5)
	_, cmd := m.Update(tea.WindowSizeMsg{Width: 30, Height: 10})
	if cmd != nil {
		t.Fatal("WindowSizeMsg returned a command; a second PTY reader would race the existing one")
	}
}
