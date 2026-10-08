package ui

import (
	"strings"

	"github.com/charmbracelet/x/ansi"
)

type Frame struct {
	Theme         Theme
	Width, Height int
}

func (f Frame) Render(title, body, help string) string {
	if f.Width <= 0 || f.Height <= 0 {
		return ""
	}
	width := min(f.Width, 80)
	footer := f.Theme.Help.Render(ansi.Truncate(help, width, ""))
	if f.Height == 1 {
		return footer
	}
	lines := []string{f.Theme.Title.Render(ansi.Truncate(title, width, ""))}
	if f.Height >= 4 {
		lines = append(lines, f.Theme.Divider.Render(strings.Repeat("─", width)))
	}
	available := f.Height - len(lines) - 1
	if available > 0 {
		bodyLines := strings.Split(ansi.Wrap(body, width, ""), "\n")
		for _, line := range bodyLines[:min(len(bodyLines), available)] {
			lines = append(lines, ansi.Truncate(line, width, ""))
		}
	}
	if len(lines) < f.Height-1 {
		lines = append(lines, "")
	}
	return strings.Join(append(lines, footer), "\n")
}
