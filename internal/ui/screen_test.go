package ui

import (
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

func TestSanitizeControls(t *testing.T) {
	cases := map[string]string{
		"\x1b[31m":           " [31m",
		"\x1b]52;c;secret\a": " ]52;c;secret ",
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
	if f.NoticeVisible(s) && !strings.Contains(got, s.Notice) {
		t.Fatalf("notice reported visible at %dx%d: %q", width, height, got)
	}
}

func checkViewSize(t *testing.T, got string, width, height int) {
	t.Helper()
	if lipgloss.Width(got) > width || lipgloss.Height(got) > height || strings.Contains(got, "\x1b") {
		t.Fatalf("%dx%d: %q", width, height, got)
	}
}
