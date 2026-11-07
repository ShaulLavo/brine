package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/ShaulLavo/brine/internal/apps"
	"github.com/ShaulLavo/brine/internal/archive"
	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/dispatch"
	"github.com/ShaulLavo/brine/internal/jobs"
	"github.com/ShaulLavo/brine/internal/plan"
	"github.com/ShaulLavo/brine/internal/result"
	"github.com/ShaulLavo/brine/internal/target"
	"github.com/ShaulLavo/brine/internal/transport"
)

func TestSQLiteRetireCommandGoldens(t *testing.T) {
	for _, command := range []string{"remove", "inspect", "resolve"} {
		for _, mode := range []string{"human", "json", "jsonl"} {
			runSQLiteRetireGolden(t, command, mode)
		}
	}
}
func runSQLiteRetireGolden(t *testing.T, command, mode string) {
	t.Helper()
	var out, stderr bytes.Buffer
	deps := testDependencies(t, &out, &stderr)
	deps.LoadOperationTarget = func(string, string) (transport.Target, error) { return transport.Target{Name: "fixture"}, nil }
	deps.OperationClient = callFunc(func(_ context.Context, _ transport.Target, req dispatch.Request) (result.Envelope, error) {
		return sqliteRetireCLIReply(t, command, req), nil
	})
	id := strings.Repeat("a", 32)
	args := []string{"remove", "hello", "--target", "fixture"}
	if command == "inspect" {
		args = []string{"archive", "sqlite", "inspect", id, "--target", "fixture"}
	}
	if command == "resolve" {
		args = []string{"archive", "sqlite", "resolve", "original", "--abandon", "--no-wait", "--idempotency-key", strings.Repeat("b", 32), "--target", "fixture"}
	}
	if mode != "human" {
		args = append(args, "--"+mode)
	}
	if err := Execute(deps, args); err != nil {
		t.Fatal(command, mode, err)
	}
	suffix := ".txt"
	if mode != "human" {
		suffix = ".json"
	}
	name := "sqlite-retire-" + command + suffix
	assertSQLiteRetireGolden(t, name, out.Bytes())
	if stderr.Len() != 0 {
		t.Fatal("unexpected stderr", stderr.String())
	}
}
func assertSQLiteRetireGolden(t *testing.T, name string, raw []byte) {
	t.Helper()
	root, err := os.OpenRoot("testdata")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = root.Close() }()
	if os.Getenv("BRINE_UPDATE_GOLDENS") == "1" {
		if err = root.WriteFile(name, raw, 0600); err != nil {
			t.Fatal(err)
		}
	}
	want, err := root.ReadFile(name)
	if err != nil || !bytes.Equal(want, raw) {
		t.Fatalf("%s mismatch: %v\ngot %s\nwant %s", name, err, raw, want)
	}
}
func sqliteRetireCLIReply(t *testing.T, command string, req dispatch.Request) result.Envelope {
	t.Helper()
	id := data.ArchiveID(strings.Repeat("a", 32))
	switch command {
	case "remove":
		if req.Op != "lifecycle" {
			t.Fatal(req.Op)
		}
		summary := archive.RemovalSummary{ArchiveID: id, ExpiresAt: time.Date(2026, 11, 7, 12, 0, 0, 0, time.UTC), RetainDays: 30, ReleaseID: "release", Route: target.CaddyFile{Name: "hello.caddy", Hash: "sha256:" + strings.Repeat("e", 64)}, Units: []target.Unit{{Name: "hello.container", Hash: "sha256:" + strings.Repeat("f", 64)}}, Databases: []archive.RemovalDatabaseSummary{{Name: "main", DatabaseID: data.DatabaseID(strings.Repeat("c", 32)), Arm: "prepared_snapshot", PreparationID: strings.Repeat("1", 32), SourcePointID: strings.Repeat("2", 32), Cutoff: time.Date(2026, 11, 7, 11, 50, 0, 0, time.UTC)}}}
		out := apps.ConfigPlan{Archive: &summary, PlanID: "sha256:" + strings.Repeat("d", 64), Kind: plan.Update, Lifecycle: plan.RemoveApp, Conflicts: []plan.Diagnostic{}}
		out.SecretRetention = "d5_retained_releases"
		return result.Success("brine host lifecycle", out)
	case "inspect":
		if req.Op != "sqlite_retire_inspect" {
			t.Fatal(req.Op)
		}
		return result.Success("brine host sqlite_retire_inspect", archive.Inspection{Preparation: &archive.PreparationInspection{Stage: archive.Intent, Sequence: 1, CanAbandon: true, ClosedReason: "continue_unavailable"}, Disposition: archive.Disposition{ArchiveID: id}, Trees: []archive.TreeInspection{{Index: 0, Location: "source"}}})
	case "resolve":
		var a dispatch.SQLiteRetireResolveArgs
		if req.Op != "sqlite_retire_resolve" || json.Unmarshal(req.Args, &a) != nil || a.OperationID != "original" || a.Disposition != "abandon" {
			t.Fatal(req)
		}
		return result.Success("brine host sqlite_retire_resolve", jobs.Accepted{Status: "accepted", OperationID: "resolver"})
	default:
		t.Fatal(command)
		return result.Envelope{}
	}
}
func TestSQLiteRetireResolveNeedsExactDisposition(t *testing.T) {
	for _, flags := range [][]string{nil, {"--continue", "--abandon"}} {
		rejectSQLiteRetireFlags(t, flags)
	}
}
func rejectSQLiteRetireFlags(t *testing.T, flags []string) {
	t.Helper()
	var out, stderr bytes.Buffer
	deps := testDependencies(t, &out, &stderr)
	deps.OperationClient = callFunc(func(context.Context, transport.Target, dispatch.Request) (result.Envelope, error) {
		t.Fatal("invalid flags reached host")
		return result.Envelope{}, nil
	})
	args := []string{"archive", "sqlite", "resolve", "original", "--target", "fixture"}
	if err := Execute(deps, append(args, flags...)); result.Classify(err).Code() != result.InvalidUsage {
		t.Fatal(err)
	}
}

func TestSQLiteRetireRemoveHelp(t *testing.T) {
	var out, stderr bytes.Buffer
	if err := Execute(testDependencies(t, &out, &stderr), []string{"remove", "--help"}); err != nil {
		t.Fatal(err)
	}
	for _, phrase := range []string{"starts nothing", "30 days", "separate reviewed"} {
		if !strings.Contains(out.String(), phrase) {
			t.Fatal("missing removal guidance", phrase, out.String())
		}
	}
}
