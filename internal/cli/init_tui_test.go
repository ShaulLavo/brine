package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ShaulLavo/brine/internal/ui"
	"github.com/charmbracelet/x/ansi"
)

func checkInitGoldens(t *testing.T) {
	t.Helper()
	preview, err := prepareInit(initInput{Directory: "fixture-project", Name: "hello", Image: initArgs(".")[5], ContainerPort: 3000, Domains: []string{"Hello.Example.com"}, HealthPath: "/"})
	if err != nil {
		t.Fatal(err)
	}
	for _, phase := range []initPhase{initReview, initConfirm} {
		t.Run(map[initPhase]string{initReview: "review", initConfirm: "confirm"}[phase], func(t *testing.T) { checkInitGoldenPhase(t, preview, phase) })
	}
}

func checkInitGoldenPhase(t *testing.T, preview initPreview, phase initPhase) {
	t.Helper()
	var got bytes.Buffer
	for _, size := range append(tuiGoldenSizes, struct{ width, height int }{1, 1}) {
		appendInitGoldenFrames(&got, preview, phase, size.width, size.height)
	}
	name := map[initPhase]string{initReview: "review", initConfirm: "confirm"}[phase]
	path := filepath.Join("testdata", "init", name+".golden")
	if os.Getenv("UPDATE_GOLDEN") == "1" {
		writeInitTestFile(t, path, got.Bytes())
	}
	want, err := readInitTestFile(t, filepath.Dir(path), filepath.Base(path))
	if err != nil || !bytes.Equal(want, got.Bytes()) {
		t.Fatalf("init golden differs: %s, %v", path, err)
	}
}

func appendInitGoldenFrames(out *bytes.Buffer, preview initPreview, phase initPhase, width, height int) {
	for _, noColor := range []bool{true, false} {
		m := newInitModel(preview, ui.NewTheme(noColor))
		m.width, m.height, m.phase = width, height, phase
		writeGoldenFrame(out, m.View().Content, width, height, noColor)
	}
}

func checkInitLayouts(t *testing.T) {
	t.Helper()
	for _, size := range []struct{ width, height int }{{80, 24}, {50, 16}, {30, 8}, {1, 1}} {
		checkInitLayoutSize(t, size.width, size.height)
	}
}

func checkInitLayoutSize(t *testing.T, width, height int) {
	t.Helper()
	for _, noColor := range []bool{true, false} {
		m := newInitModel(initFixture(t), ui.NewTheme(noColor))
		m.width, m.height = width, height
		m.preview.Directory = "\x1b]52;c;PLANTED\a" + strings.Repeat("long-界é", 40)
		checkInitLayoutModel(t, m)
	}
}

func checkInitLayoutModel(t *testing.T, m initModel) {
	t.Helper()
	m.phase = initConfirm
	text := m.View().Content
	lines := strings.Split(text, "\n")
	if len(lines) != m.height {
		t.Fatalf("height %d at %dx%d", len(lines), m.width, m.height)
	}
	for _, line := range lines {
		if ansi.StringWidth(line) > m.width {
			t.Fatalf("line too wide at %dx%d: %q", m.width, m.height, line)
		}
	}
	if strings.Contains(text, "\x1b]52;") || strings.Contains(text, "\a") {
		t.Fatal("unsafe terminal control")
	}
	next, command := m.key("y")
	if next.(initModel).phase == initApproved || command != nil {
		t.Fatal("truncated confirmation approved")
	}
}
