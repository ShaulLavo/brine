package ui

import (
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
)

func TestFrameRender(t *testing.T) {
	for _, size := range []struct{ width, height int }{{80, 24}, {24, 8}, {8, 4}, {1, 1}, {80, 2}, {0, 0}, {-1, -1}} {
		frame := Frame{Theme: NewTheme(true), Width: size.width, Height: size.height}
		got := frame.Render("brine  /  deployment control", "A foundation, not yet a deployment engine.\n界面 stays within the terminal.", "Press q to quit.")
		if size.width <= 0 || size.height <= 0 {
			if got != "" {
				t.Fatalf("size %v: expected empty frame, got %q", size, got)
			}
			continue
		}
		if lipgloss.Height(got) > size.height || lipgloss.Width(got) > size.width {
			t.Fatalf("size %v: frame exceeds terminal: %q", size, got)
		}
		if size.width >= 24 && !strings.Contains(got, "Press q to quit.") {
			t.Fatalf("size %v: footer missing: %q", size, got)
		}
		if size.width == 80 && size.height == 24 {
			for _, text := range []string{"brine  /  deployment control", "A foundation, not yet a deployment engine."} {
				if !strings.Contains(got, text) {
					t.Fatalf("normal frame missing %q: %q", text, got)
				}
			}
		}
	}
}

func TestFrameWideGraphemesAtTinyWidths(t *testing.T) {
	for _, body := range []string{"界面 stays within the terminal.", "🌊 waves", "👩‍💻 coding", "🇯🇵 flag"} {
		t.Run(body, func(t *testing.T) {
			for _, size := range []struct{ width, height int }{{1, 5}, {1, 12}, {2, 12}, {3, 12}} {
				for _, noColor := range []bool{false, true} {
					frame := Frame{Theme: NewTheme(noColor), Width: size.width, Height: size.height}
					got := frame.Render("brine", body, "Press q to quit.")
					if lipgloss.Width(got) > size.width || lipgloss.Height(got) > size.height {
						t.Fatalf("size %v, noColor %t: frame exceeds terminal: %q", size, noColor, got)
					}
				}
			}
		})
	}
}

func TestThemeNoColor(t *testing.T) {
	for _, value := range []string{"1", "true", "0", ""} {
		t.Run("NO_COLOR="+value, func(t *testing.T) {
			t.Setenv("NO_COLOR", value)
			frame := Frame{Theme: ThemeFromEnv(), Width: 80, Height: 24}
			got := frame.Render("brine", "Welcome", "Press q to quit.")
			if colored := strings.Contains(got, "\x1b["); colored != (value == "") {
				t.Fatalf("NO_COLOR=%q: unexpected styling: %q", value, got)
			}
		})
	}
}
