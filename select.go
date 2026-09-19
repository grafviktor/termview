package termview

import (
	"strings"

	uv "github.com/charmbracelet/ultraviolet"
)

func (m Model) hasSelection() bool {
	if !m.isSelecting {
		return false
	}

	// Check that start and end coordinates are not the same. If they are, then there is no selection.
	// We do not support single character selection, because on every mouse click, the will be a selected
	// cell which looks like another cursor. Consider this as a feature, at lest for now.
	return m.startX != m.endX || m.startY != m.endY
}

func normalize(sx, sy, ex, ey int) (x1, y1, x2, y2 int) {
	x1, y1, x2, y2 = sx, sy, ex, ey
	// If user selected from bottom to top
	if y1 > y2 {
		return x2, y2, x1, y1
	}
	// If user selected from right to left
	if y1 == y2 && x1 > x2 {
		return x2, y2, x1, y1
	}
	// Otherwise, return as is
	return x1, y1, x2, y2
}

func inSelection(x, y, left, top, right, bottom int) bool {
	// If beyond selection bounds, return false.
	if y < top || y > bottom {
		return false
	}
	// If on the same line, check if x is within the selection bounds.
	if top == bottom {
		return left <= x && x <= right
	}
	// If on the top line, check if x is to the right of the left bound.
	if y == top {
		return left <= x
	}
	// If on the bottom line, check if x is to the left of the right bound.
	if y == bottom {
		return x <= right
	}
	// Otherwise, it's a full middle line.
	return true
}

func (m Model) bufferCell(x, bufY int) *uv.Cell {
	if m.emu == nil || x < 0 || x >= m.width || bufY < 0 {
		return nil
	}

	sbLen := m.emu.ScrollbackLen()
	if bufY < sbLen {
		return m.emu.ScrollbackCellAt(x, bufY)
	}
	return m.emu.CellAt(x, bufY-sbLen)
}

func (m Model) selectedText() string {
	left, top, right, bottom := normalize(m.startX, m.startY, m.endX, m.endY)
	lines := []string{}
	for bufY := top; bufY <= bottom; bufY++ {
		selectedLine := false
		var str strings.Builder
		for x := 0; x < m.width; {
			cell := m.bufferCell(x, bufY)
			if cell == nil {
				break
			}
			if inSelection(x, bufY, left, top, right, bottom) {
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

func (m Model) viewWithSelection() string {
	left, top, right, bottom := normalize(m.startX, m.startY, m.endX, m.endY)
	lines := make([]string, 0, m.height)
	origin := m.emu.ScrollbackLen() - m.scrollOffset
	for y := 0; y < m.height; y++ {
		yRelativeToScrollback := origin + y
		line := make(uv.Line, 0, m.width)
		for x := 0; x < m.width; {
			cell := m.bufferCell(x, yRelativeToScrollback)
			if cell == nil {
				line = append(line, uv.EmptyCell)
				x++
				continue
			}
			c := *cell
			if c.Width <= 0 {
				c.Width = 1
			}
			if inSelection(x, yRelativeToScrollback, left, top, right, bottom) {
				c.Style.Attrs |= uv.AttrReverse
			}
			line = append(line, c)
			x += c.Width
		}
		lines = append(lines, line.Render())
	}
	return strings.Join(lines, "\n")
}
