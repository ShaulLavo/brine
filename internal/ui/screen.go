package ui

import (
	"strings"

	"github.com/charmbracelet/x/ansi"
)

// Screen is one full-terminal frame. Body lines are plain text that Screen
// sanitizes. Table rows follow the body and fill the remaining rows; Table
// sanitizes its own cells, so its styling survives.
type Screen struct {
	Title, Context string
	Body           []string
	Table          *Table
	Notice         string
	NoticeTone     Tone
	Help           []string
}

// maxNoticeRows bounds a wrapped notice so the body keeps most of the screen.
const maxNoticeRows = 3

// NoticeVisible reports whether the complete sanitized notice is displayed.
// Callers can use it to refuse a confirmation whose identities do not fit.
func (f Frame) NoticeVisible(s Screen) bool {
	return s.Notice != "" && f.Width > 0 && len(f.wrapNotice(s)) <= f.noticeRoom()
}

// BodyRows returns how many body and table rows Screen displays for s.
func (f Frame) BodyRows(s Screen) int {
	return max(0, f.Height-f.headerRows()-1-len(f.noticeLines(s)))
}

// noticeRoom is how many rows a notice may take: never the title or help row.
func (f Frame) noticeRoom() int {
	return min(maxNoticeRows, f.Height-f.headerRows()-1)
}

func (f Frame) wrapNotice(s Screen) []string {
	return strings.Split(ansi.Wrap(Sanitize(s.Notice), f.Width, ""), "\n")
}

// noticeLines wraps the notice to the width. When it needs more rows than it
// may take, the last row shown ends with an ellipsis.
func (f Frame) noticeLines(s Screen) []string {
	room := f.noticeRoom()
	if s.Notice == "" || room < 1 {
		return nil
	}
	lines := f.wrapNotice(s)
	if len(lines) > room {
		rest := strings.Join(lines[room-1:], " ")
		lines = append(lines[:room-1:room-1], ansi.Truncate(rest, max(0, f.Width-1), "")+"…")
	}
	for i, line := range lines {
		lines[i] = ansi.Truncate(strings.TrimRight(line, " "), f.Width, "")
	}
	return lines
}

func (f Frame) headerRows() int {
	switch {
	case f.Height >= 4:
		return 2
	case f.Height >= 2:
		return 1
	default:
		return 0
	}
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
	lines := []string{f.header(s)}
	if f.Height >= 4 {
		lines = append(lines, f.Theme.Divider.Render(strings.Repeat("─", width)))
	}
	notice := f.noticeLines(s)
	available := f.BodyRows(s)
	body := s.Body[:min(len(s.Body), available)]
	for _, line := range body {
		lines = append(lines, ansi.Truncate(Sanitize(line), width, ""))
	}
	if s.Table != nil {
		lines = append(lines, s.Table.Lines(f.Theme, width, available-len(body))...)
	}
	for len(lines) < f.Height-1-len(notice) {
		lines = append(lines, "")
	}
	for _, line := range notice {
		lines = append(lines, f.Theme.Style(s.NoticeTone).Render(line))
	}
	return strings.Join(append(lines, f.Theme.Help.Render(help)), "\n")
}

// header is the title on the left and, when both fit, muted context on the right.
func (f Frame) header(s Screen) string {
	title := Sanitize(s.Title)
	context := Sanitize(s.Context)
	gap := f.Width - ansi.StringWidth(title) - ansi.StringWidth(context)
	if context == "" || gap <= 0 {
		return f.Theme.Title.Render(ansi.Truncate(title, f.Width, ""))
	}
	return f.Theme.Title.Render(title) + strings.Repeat(" ", gap) + f.Theme.Help.Render(context)
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
