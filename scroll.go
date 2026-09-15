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

// followScrollback keeps the viewport on the same lines while the live screen
// keeps scrolling. scrollOffset is measured from the bottom of the scrollback,
// so each new line would otherwise push the view toward newer content.
func (m *Model) followScrollback() {
	sbLen := 0
	if m.emu != nil {
		sbLen = m.emu.ScrollbackLen()
	}
	if m.scrollOffset > 0 {
		m.scrollTo(m.scrollOffset + (sbLen - m.lastScrollbackLen))
	}
	m.lastScrollbackLen = sbLen
}

// maxScrollOffset is the offset that puts the oldest scrollback line on the
// first row of the viewport. The alternate screen keeps no scrollback of its
// own, so full screen apps such as editors and pagers never scroll.
func (m Model) maxScrollOffset() int {
	if m.emu == nil || m.emu.IsAltScreen() {
		return 0
	}
	return m.emu.ScrollbackLen()
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

// scrollbackLine copies the scrollback line at index out of the emulator. The
// line is read cell by cell because that is the only concurrency-safe way to
// reach it while the PTY goroutine keeps writing to the emulator.
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
