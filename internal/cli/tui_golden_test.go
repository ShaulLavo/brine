package cli

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
	"github.com/ShaulLavo/brine/internal/apps"
	"github.com/ShaulLavo/brine/internal/logs"
	"github.com/ShaulLavo/brine/internal/result"
	"github.com/ShaulLavo/brine/internal/ui"
	"github.com/charmbracelet/x/ansi"
)

// tuiGoldenStates covers loading, empty, unknown, ready and failed views of
// every read-only screen. All values are synthetic fixtures.
func tuiGoldenStates() map[string]tuiModel {
	transportFailure := result.TransportError(errors.New("ssh: fixture refused"), result.TransportDiagnostics{Operation: "status", Reason: "exit"})
	base := fixtureModel(fixtureHost(), "")
	base.cancelRead()
	base.targets = loadable[[]string]{}
	targets := func(state loadState, names []string, err error) tuiModel {
		m := base
		m.targets.start()
		if state != loadLoading {
			m.targets.finish(names, err, fixtureReadTime)
		}
		return m
	}
	onApps := func(screen tuiScreen, app string, finish ...func(*loadable[apps.Report])) tuiModel {
		m := base
		m.flags = operationFlags{target: "fixture"}
		m.screen, m.app = screen, app
		m.apps.start()
		for _, f := range finish {
			f(&m.apps)
		}
		return m
	}
	ready := func(l *loadable[apps.Report]) { l.finish(fixtureReport(), nil, fixtureReadTime) }
	empty := func(l *loadable[apps.Report]) { l.finish(apps.Report{Apps: []apps.Status{}}, nil, fixtureReadTime) }
	failed := func(l *loadable[apps.Report]) { l.finish(apps.Report{}, transportFailure, fixtureReadTime) }
	restart := func(l *loadable[apps.Report]) { l.start() }
	onLogs := func(state loadState, lines []logs.Line, err error) tuiModel {
		m := onApps(screenLogs, "fixture-api", ready)
		m.back = []tuiScreen{screenApps, screenApp}
		m.logs.start()
		if state != loadLoading {
			m.logs.finish(lines, err, fixtureReadTime)
		}
		return m
	}
	states := map[string]tuiModel{
		"targets-loading":  targets(loadLoading, nil, nil),
		"targets-empty":    targets(loadReady, []string{}, nil),
		"targets-ready":    targets(loadReady, []string{"fixture", "fixture-pi"}, nil),
		"targets-failed":   targets(loadFailed, nil, errors.New("permission denied")),
		"apps-loading":     onApps(screenApps, ""),
		"apps-empty":       onApps(screenApps, "", empty),
		"apps-ready":       onApps(screenApps, "", ready),
		"apps-failed":      onApps(screenApps, "", failed),
		"apps-stale":       onApps(screenApps, "", ready, restart, failed),
		"app-loading":      onApps(screenApp, "fixture-api"),
		"app-ready":        onApps(screenApp, "fixture-api", ready),
		"app-unknown":      onApps(screenApp, "fixture-jobs", ready),
		"app-missing":      onApps(screenApp, "fixture-old", ready),
		"app-failed":       onApps(screenApp, "fixture-api", failed),
		"logs-loading":     onLogs(loadLoading, nil, nil),
		"logs-empty":       onLogs(loadReady, []logs.Line{}, nil),
		"logs-ready":       onLogs(loadReady, fixtureLogLines(), nil),
		"logs-failed":      onLogs(loadFailed, nil, transportFailure),
		"apps-refreshing":  onApps(screenApps, "", ready, restart),
		"targets-selected": withTarget(targets(loadReady, []string{"fixture", "fixture-pi"}, nil), 1),
	}
	for name, m := range tuiOperationGoldenStates() {
		states[name] = m
	}
	return states
}

func withTarget(m tuiModel, i int) tuiModel {
	m.target = i
	return m
}

var tuiGoldenSizes = []struct{ width, height int }{{80, 24}, {50, 16}, {30, 8}}

func TestTUIGoldens(t *testing.T) {
	for name, m := range tuiGoldenStates() {
		t.Run(name, func(t *testing.T) {
			var got bytes.Buffer
			for _, size := range tuiGoldenSizes {
				for _, noColor := range []bool{true, false} {
					m.width, m.height, m.theme = size.width, size.height, ui.NewTheme(noColor)
					writeGoldenFrame(&got, view(m), size.width, size.height, noColor)
				}
			}
			path := filepath.Join("testdata", "tui", name+".golden")
			if os.Getenv("UPDATE_GOLDEN") == "1" {
				if err := os.WriteFile(path, got.Bytes(), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			want, err := os.ReadFile(path) // #nosec G304 -- Fixed testdata directory and a state name from this test.
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(want, got.Bytes()) {
				t.Fatalf("%s differs; rerun with UPDATE_GOLDEN=1 after review.\nwant:\n%s\ngot:\n%s", path, want, got.String())
			}
		})
	}
}

// writeGoldenFrame draws no-color frames inside a right border so trailing
// space is visible, and quotes colored lines so escapes stay reviewable.
func writeGoldenFrame(out *bytes.Buffer, frame string, width, height int, noColor bool) {
	mode := "color"
	if noColor {
		mode = "no color"
	}
	fmt.Fprintf(out, "== %dx%d %s ==\n", width, height, mode)
	for _, line := range strings.Split(frame, "\n") {
		if !noColor {
			out.WriteString(strconv.Quote(line) + "\n")
			continue
		}
		out.WriteString(line + strings.Repeat(" ", max(0, width-ansi.StringWidth(line))) + "│\n")
	}
}

// TestTUIViewsFitEveryTerminal sweeps every state across terminal sizes,
// including 1x1, in both color modes.
func TestTUIViewsFitEveryTerminal(t *testing.T) {
	heights := []int{1, 2, 3, 4, 5, 6, 7, 8, 9, 12, 16, 24, 40}
	for name, m := range tuiGoldenStates() {
		for width := 1; width <= 120; width++ {
			for _, height := range heights {
				for _, noColor := range []bool{true, false} {
					m.width, m.height, m.theme = width, height, ui.NewTheme(noColor)
					got := view(m)
					if lipgloss.Width(got) > width || lipgloss.Height(got) > height {
						t.Fatalf("%s at %dx%d exceeds the terminal: %q", name, width, height, got)
					}
					if noColor && strings.Contains(got, "\x1b") {
						t.Fatalf("%s at %dx%d has ESC without color: %q", name, width, height, got)
					}
				}
			}
		}
	}
}
