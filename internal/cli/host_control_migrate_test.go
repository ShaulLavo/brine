package cli

import (
	"context"
	"io"
	"strings"
	"testing"

	"github.com/ShaulLavo/brine/internal/admission"
)

func TestControlMigrateExactSelectorBeforeRuntime(t *testing.T) {
	id, attempt := strings.Repeat("a", 32), strings.Repeat("b", 32)
	for _, args := range [][]string{
		{"update", id, "migrating", attempt},
		{"control-restore", id, "migrating", attempt},
		{"update", id, "opening", attempt},
		{"update", strings.ToUpper(id), "migrating", attempt},
		{"update", id, "migrating", "bad"},
		{"update", id, "migrating"},
		{"update", id, "migrating", attempt, "extra"},
		{"update", id, "migrating", attempt, "--root=/tmp"},
		{"update", id, "migrating", attempt, "--help"},
	} {
		testControlMigrateArgs(t, args, id, attempt)
	}
}

func testControlMigrateArgs(t *testing.T, args []string, id, attempt string) {
	t.Helper()
	calls := 0
	dependencies := Dependencies{Context: context.Background(), Stdin: strings.NewReader(""), Stdout: io.Discard, Stderr: io.Discard}
	lifecycle := RuntimeLifecycle{ControlMigrate: func(_ context.Context, selector admission.Exemption) error {
		calls++
		if selector != (admission.Exemption{Kind: admission.Update, ID: id, Stage: admission.Migrating, Attempt: attempt}) {
			t.Fatal(selector)
		}
		return nil
	}, Open: func(context.Context, bool) (RuntimeServices, error) {
		t.Fatal("opened ordinary runtime")
		return RuntimeServices{}, nil
	}}
	err := ExecuteWithRuntime(dependencies, append([]string{"host", "control-migrate"}, args...), lifecycle)
	valid := strings.Join(args, " ") == "update "+id+" migrating "+attempt
	if valid && (err != nil || calls != 1) || !valid && calls != 0 {
		t.Fatal(args, calls, err)
	}
}

func TestUDPGrantCheckUsesHeldReaderPort(t *testing.T) {
	calls := 0
	dependencies := Dependencies{Context: context.Background(), Stdin: strings.NewReader(""), Stdout: io.Discard, Stderr: io.Discard}
	lifecycle := RuntimeLifecycle{UDPGrantHeld: func(_ context.Context, app string) (bool, error) {
		calls++
		if app != "example" {
			t.Fatal(app)
		}
		return true, nil
	}}
	if err := ExecuteWithRuntime(dependencies, []string{"host", "udp-grant-check", "example"}, lifecycle); err != nil || calls != 1 {
		t.Fatal(calls, err)
	}
	if err := ExecuteWithRuntime(dependencies, []string{"host", "udp-grant-check"}, lifecycle); err == nil || calls != 1 {
		t.Fatal(calls, err)
	}
}
