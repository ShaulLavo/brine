package cli

import (
	"bytes"
	"context"
	"io"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/ShaulLavo/brine/internal/transport"
)

func TestInitHumanHintEntersTUI(t *testing.T) {
	directory := t.TempDir()
	var output, diagnostics bytes.Buffer
	deps := initNoHostDependencies(t, &output, &diagnostics)
	deps.LoadOperationTarget = func(string, string) (transport.Target, error) {
		return transport.Target{Name: "fixture"}, nil
	}
	args := append(initArgs(directory), "--target", "fixture", "--yes")
	if err := Execute(deps, args); err != nil {
		t.Fatal(err)
	}
	next := ""
	for _, line := range strings.Split(output.String(), "\n") {
		if value, ok := strings.CutPrefix(line, "Next: "); ok {
			next = value
		}
	}
	if !strings.HasPrefix(next, "brine tui --spec ") {
		t.Fatal("human init must advertise the connected TUI entry", next)
	}
	entered := false
	deps.RunTUI = func(_ context.Context, model tea.Model, _ io.Reader, _ io.Writer) (tea.Model, error) {
		m, ok := model.(tuiModel)
		if !ok || m.entry.Spec != directory+"/brine.toml" || m.flags.target != "fixture" {
			t.Fatal("next command lost its generated spec or selection")
		}
		entered = true
		return model, nil
	}
	withTestTerminal(t, &deps)
	if err := Execute(deps, strings.Fields(strings.TrimPrefix(next, "brine "))); err != nil || !entered {
		t.Fatal("next command did not enter the TUI", err)
	}
}
