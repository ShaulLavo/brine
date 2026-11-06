package ui

import (
	"strings"

	"github.com/charmbracelet/x/ansi"
)

type Column struct {
	Title              string
	Min, Max, Priority int
}
type Cell struct {
	Text string
	Tone Tone
}
type Table struct {
	Columns  []Column
	Rows     [][]Cell
	Selected int
}

// Window returns a half-open range containing the selected item when possible.
func Window(total, selected, height int) (int, int) {
	if total <= 0 || height <= 0 {
		return 0, 0
	}
	selected = max(0, min(selected, total-1))
	height = min(height, total)
	start := max(0, selected-height+1)
	return start, start + height
}

func (t Table) Lines(theme Theme, width, height int) []string {
	if width <= 0 || height <= 0 || len(t.Columns) == 0 {
		return nil
	}
	cols := t.visibleColumns(width)
	if len(cols) == 0 {
		return nil
	}
	widths := t.columnWidths(cols, width)
	if height == 1 && len(t.Rows) > 0 {
		i := max(0, min(t.Selected, len(t.Rows)-1))
		return []string{t.row(theme, cols, widths, t.Rows[i], true, false, width)}
	}
	start, end := Window(len(t.Rows), t.Selected, max(0, height-1))
	lines := []string{t.row(theme, cols, widths, nil, false, true, width)}
	for i := start; i < end; i++ {
		lines = append(lines, t.row(theme, cols, widths, t.Rows[i], i == t.Selected, false, width))
	}
	return lines
}

func (t Table) visibleColumns(width int) []int {
	cols := make([]int, len(t.Columns))
	for i := range cols {
		cols[i] = i
	}
	for len(cols) > 1 && t.minimumWidth(cols) > width {
		drop := 0
		for i := 1; i < len(cols); i++ {
			if t.Columns[cols[i]].Priority < t.Columns[cols[drop]].Priority {
				drop = i
			}
		}
		cols = append(cols[:drop], cols[drop+1:]...)
	}
	return cols
}

func (t Table) minimumWidth(cols []int) int {
	n := 2 + 2*(len(cols)-1)
	for _, i := range cols {
		n += max(1, t.Columns[i].Min)
	}
	return n
}

func (t Table) columnWidths(cols []int, width int) []int {
	widths := make([]int, len(cols))
	remaining := max(0, width-2-2*(len(cols)-1))
	for j, i := range cols {
		widths[j] = min(remaining, max(1, t.Columns[i].Min))
		remaining -= widths[j]
	}
	for j, i := range cols {
		extra := min(remaining, max(0, t.Columns[i].Max-widths[j]))
		widths[j] += extra
		remaining -= extra
	}
	return widths
}

func (t Table) row(theme Theme, cols, widths []int, cells []Cell, selected, header bool, width int) string {
	marker := "  "
	if selected {
		marker = "› "
	}
	parts := []string{theme.Style(Accent).Render(marker)}
	for j, i := range cols {
		text := ""
		tone := Plain
		if header {
			text = t.Columns[i].Title
			tone = Muted
		} else if i < len(cells) {
			text = cells[i].Text
			tone = cells[i].Tone
		}
		text = ansi.Truncate(Sanitize(text), widths[j], "…")
		if j < len(cols)-1 {
			text += strings.Repeat(" ", max(0, widths[j]-ansi.StringWidth(text))+2)
		}
		style := theme.Style(tone)
		if selected && !theme.noColor {
			style = style.Bold(true)
		}
		parts = append(parts, style.Render(text))
	}
	return ansi.Truncate(strings.Join(parts, ""), width, "")
}
