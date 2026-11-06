package ui

import (
	"regexp"
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
)

func TestScreenSweepAndNoticeVisibility(t *testing.T) {
	s := Screen{Title: "界 👩‍💻", Context: "context", Body: []string{"🇯🇵 hello\x1b[31m"}, Notice: "Confirm: restart long-app on target", Help: []string{"enter open", "q quit"}}
	for w := 1; w <= 120; w++ {
		for h := 1; h <= 40; h++ {
			checkScreenSize(t, s, w, h)
		}
	}
	if (Frame{Width: 10, Height: 8}).NoticeVisible(s) || !(Frame{Width: 80, Height: 8}).NoticeVisible(s) {
		t.Fatal("notice visibility")
	}
}

func TestScreenTableKeepsStylingAndFillsBodyRows(t *testing.T) {
	table := Table{Columns: []Column{{Title: "APP", Min: 4, Max: 12, Priority: 2}, {Title: "STATE", Min: 6, Max: 9, Priority: 1}}, Rows: [][]Cell{{{Text: "one\x1b]52;c;x\a"}, {Text: "failed", Tone: Bad}}, {{Text: "two"}, {Text: "ok", Tone: Good}}}, Selected: 1}
	s := Screen{Title: "brine", Body: []string{"intro"}, Table: &table, Notice: "Error: x", Help: []string{"q quit"}}
	for _, noColor := range []bool{false, true} {
		f := Frame{Theme: NewTheme(noColor), Width: 40, Height: 10}
		got := f.Screen(s)
		plain := ansiPattern.ReplaceAllString(got, "")
		if strings.Contains(plain, "[3") || strings.Contains(plain, "]52") && !strings.Contains(plain, " ]52;c;x ") || !strings.Contains(plain, "› two") {
			t.Fatalf("noColor=%t: table styling or sanitizing broke: %q", noColor, got)
		}
		if noColor == strings.Contains(got, "\x1b[") {
			t.Fatalf("noColor=%t: styling mismatch: %q", noColor, got)
		}
		// Title, divider, intro, header, two rows, padding, notice, help.
		if rows := f.BodyRows(s); rows != 6 || lipgloss.Height(got) != 10 {
			t.Fatalf("body rows %d, height %d", rows, lipgloss.Height(got))
		}
	}
	// Height: 2-3 rows keep the title only; from 4 rows the divider appears.
	for h, want := range map[int]int{2: 0, 3: 0, 4: 0, 5: 1, 12: 8} {
		f := Frame{Theme: NewTheme(true), Width: 30, Height: h}
		if got := f.BodyRows(s); got != want {
			t.Fatalf("height %d: body rows %d, want %d: %q", h, got, want, f.Screen(s))
		}
	}
}

func TestScreenWrapsLongNotice(t *testing.T) {
	s := Screen{Title: "brine", Body: []string{"body"}, Notice: "Error: transport_failure. The SSH transport failed; reconcile before retrying any mutation.", Help: []string{"q quit"}}
	wide := Frame{Theme: NewTheme(true), Width: 120, Height: 10}
	if !wide.NoticeVisible(s) || !strings.Contains(wide.Screen(s), s.Notice) || wide.BodyRows(s) != 6 {
		t.Fatalf("wide notice: %q", wide.Screen(s))
	}
	narrow := Frame{Theme: NewTheme(true), Width: 50, Height: 10}
	got := narrow.Screen(s)
	if !narrow.NoticeVisible(s) || !strings.Contains(got, "Error: transport_failure. The SSH transport\nfailed; reconcile before retrying any mutation.") || narrow.BodyRows(s) != 5 {
		t.Fatalf("wrapped notice: %q", got)
	}
	tiny := Frame{Theme: NewTheme(true), Width: 20, Height: 10}
	got = tiny.Screen(s)
	lines := strings.Split(got, "\n")
	if tiny.NoticeVisible(s) || !strings.HasSuffix(lines[len(lines)-2], "…") || tiny.BodyRows(s) != 4 {
		t.Fatalf("clipped notice must end with an ellipsis and report not visible: %q", got)
	}
}

var ansiPattern = regexp.MustCompile("\x1b\\[[0-9;]*m")

func TestSanitizeControls(t *testing.T) {
	cases := map[string]string{
		"\x1b[31m":           " [31m",
		"\x1b]52;c;pasted\a": " ]52;c;pasted ",
		"a\u202eb":           "a b",
		"a\u009bb":           "a b",
	}
	for input, want := range cases {
		if got := Sanitize(input); got != want {
			t.Errorf("Sanitize(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestTablePriorityAndWindow(t *testing.T) {
	table := Table{Columns: []Column{{Title: "NAME", Min: 5, Max: 5, Priority: 3}, {Title: "LOW", Min: 4, Max: 4, Priority: 1}, {Title: "STATE", Min: 5, Max: 5, Priority: 2}}, Rows: [][]Cell{{{Text: "one"}, {Text: "low"}, {Text: "good"}}, {{Text: "two"}, {Text: "low"}, {Text: "bad"}}, {{Text: "three"}, {Text: "low"}, {Text: "ok"}}}, Selected: 2}
	got := strings.Join(table.Lines(NewTheme(true), 16, 2), "\n")
	if strings.Contains(got, "LOW") || !strings.Contains(got, "STATE") || !strings.Contains(got, "› three") {
		t.Fatalf("%q", got)
	}
	start, end := Window(10, 9, 3)
	if start != 7 || end != 10 {
		t.Fatalf("%d %d", start, end)
	}
}

func TestTableSizeSweep(t *testing.T) {
	table := Table{Columns: []Column{{Title: "界", Min: 2, Max: 10, Priority: 2}, {Title: "state", Min: 2, Max: 8, Priority: 1}}, Rows: [][]Cell{{{Text: "👩‍💻"}, {Text: "ok"}}, {{Text: "🇯🇵"}, {Text: "failed"}}}, Selected: 1}
	for w := 1; w <= 120; w++ {
		for h := 1; h <= 40; h++ {
			got := strings.Join(table.Lines(NewTheme(true), w, h), "\n")
			checkViewSize(t, got, w, h)
		}
	}
}

func TestTableSelectionClampsBeforeRendering(t *testing.T) {
	table := Table{Columns: []Column{{Title: "NAME", Min: 5, Max: 5}}, Rows: [][]Cell{{{Text: "one"}}, {{Text: "two"}}}, Selected: -1}
	if got := strings.Join(table.Lines(NewTheme(true), 10, 3), "\n"); !strings.Contains(got, "› one") {
		t.Fatalf("negative selection lost marker: %q", got)
	}
	table.Selected = 99
	if got := strings.Join(table.Lines(NewTheme(true), 10, 3), "\n"); !strings.Contains(got, "› two") {
		t.Fatalf("past-end selection lost marker: %q", got)
	}
}

func checkScreenSize(t *testing.T, s Screen, width, height int) {
	t.Helper()
	f := Frame{Theme: NewTheme(true), Width: width, Height: height}
	got := f.Screen(s)
	checkViewSize(t, got, width, height)
	// A visible notice may wrap at spaces or hyphens, so compare without whitespace.
	squash := func(text string) string { return strings.Join(strings.Fields(text), "") }
	if f.NoticeVisible(s) && !strings.Contains(squash(got), squash(s.Notice)) {
		t.Fatalf("notice reported visible at %dx%d: %q", width, height, got)
	}
}

func checkViewSize(t *testing.T, got string, width, height int) {
	t.Helper()
	if lipgloss.Width(got) > width || lipgloss.Height(got) > height || strings.Contains(got, "\x1b") {
		t.Fatalf("%dx%d: %q", width, height, got)
	}
}
