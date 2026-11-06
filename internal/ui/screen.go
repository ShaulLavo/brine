package ui

import (
	"strings"

	"github.com/charmbracelet/x/ansi"
)

type Screen struct {
	Title, Context string
	Body           []string
	Notice         string
	NoticeTone     Tone
	Help           []string
}

// NoticeVisible reports whether the complete sanitized notice is displayed.
// Callers can use it to refuse a confirmation whose identities do not fit.
func (f Frame) NoticeVisible(s Screen) bool {
	return f.Width > 0 && f.Height >= 3 && s.Notice != "" && ansi.StringWidth(Sanitize(s.Notice)) <= f.Width
}

func (f Frame) Screen(s Screen) string {
	if f.Width <= 0 || f.Height <= 0 {
		return ""
	}
	width := f.Width
	help := helpLine(s.Help, width)
	if f.Height == 1 {
		return f.Theme.Help.Render(help)
	}
	title := Sanitize(s.Title)
	context := Sanitize(s.Context)
	gap := width - ansi.StringWidth(title) - ansi.StringWidth(context)
	if context != "" && gap > 0 {
		title += strings.Repeat(" ", gap) + context
	}
	lines := []string{f.Theme.Title.Render(ansi.Truncate(title, width, ""))}
	if f.Height >= 4 {
		lines = append(lines, f.Theme.Divider.Render(strings.Repeat("─", width)))
	}
	notice := s.Notice != "" && f.Height-len(lines) >= 2
	available := f.Height - len(lines) - 1
	if notice {
		available--
	}
	for _, line := range s.Body[:min(len(s.Body), max(0, available))] {
		lines = append(lines, ansi.Truncate(Sanitize(line), width, ""))
	}
	for len(lines) < f.Height-1-btoi(notice) {
		lines = append(lines, "")
	}
	if notice {
		lines = append(lines, f.Theme.Style(s.NoticeTone).Render(ansi.Truncate(Sanitize(s.Notice), width, "")))
	}
	return strings.Join(append(lines, f.Theme.Help.Render(help)), "\n")
}

func btoi(b bool) int {
	if b {
		return 1
	}
	return 0
}

func helpLine(items []string, width int) string {
	line := ""
	for _, item := range items {
		separator := ""
		if line != "" {
			separator = "  "
		}
		candidate := line + separator + Sanitize(item)
		if ansi.StringWidth(candidate) > width {
			break
		}
		line = candidate
	}
	return line
}
