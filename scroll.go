package termview

import (
	"strings"

	uv "github.com/charmbracelet/ultraviolet"
)

// mouseScrollStep is the number of lines a single mouse wheel tick scrolls.
const mouseScrollStep = 3

func (m *Model) scrollUp(lines int) {
	m.scrollTo(m.scrollOffset + lines)
}

func (m *Model) scrollDown(lines int) {
	m.scrollTo(m.scrollOffset - lines)
}

func (m *Model) scrollTo(offset int) {
	m.scrollOffset = min(max(offset, 0), m.maxScrollOffset())
}

func (m *Model) followScrollback() {
	sbLen := 0
	if m.emu != nil {
		sbLen = m.emu.ScrollbackLen()
	}
	growth := sbLen - m.lastScrollbackLen
	if m.scrollOffset > 0 {
		m.scrollTo(m.scrollOffset + growth)
	}
	// Scrollback indices shift down when old lines are dropped. Keep an
	// in-progress selection on the same text.
	if m.isSelecting && growth < 0 {
		m.startY = max(m.startY+growth, 0)
		m.endY = max(m.endY+growth, 0)
	}
	m.lastScrollbackLen = sbLen
}

func (m Model) maxScrollOffset() int {
	if m.emu == nil || m.emu.IsAltScreen() {
		return 0
	}
	return m.emu.ScrollbackLen()
}

func (m Model) scrollbackLine(index int) uv.Line {
	line := make(uv.Line, 0, m.width)
	for x := range m.width {
		cell := m.emu.ScrollbackCellAt(x, index)
		if cell == nil {
			// Trailing blanks are trimmed when a line enters the scrollback.
			break
		}
		line = append(line, *cell)
	}

	return line
}

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
