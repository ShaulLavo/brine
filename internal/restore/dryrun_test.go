package restore

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type observationCLI func(context.Context, Command) (CommandResult, error)

func (f observationCLI) Execute(ctx context.Context, c Command) (CommandResult, error) {
	return f(ctx, c)
}

func watermarkInputs(t *testing.T) (string, Binding, Credentials) {
	t.Helper()
	root := t.TempDir()
	// #nosec G302 -- Private workspace directories require owner traversal.
	mustRestoreTest(t, os.Chmod(root, 0700))
	d, c := probeInputs("https://storage.example.test")
	return root, Binding{ID: "b1", Epoch: "e1", CredentialRef: "c1", Destination: d}, c
}

func dryRunOutput(t *testing.T, c Command) string {
	t.Helper()
	output := ""
	for i, arg := range c.Args {
		if arg == "-o" && i+1 < len(c.Args) {
			output = c.Args[i+1]
		}
	}
	return output
}

func exactPlan(output, txid string) []byte {
	raw, _ := json.Marshal(map[string]string{"target_path": output, "replica": "s3", "max_txid": txid})
	return raw
}

func assertWorkspaceEmpty(t *testing.T, root string) {
	t.Helper()
	entries, err := os.ReadDir(root)
	if err != nil || len(entries) != 0 {
		t.Fatalf("workspace not disposed: entries=%v err=%v", entries, err)
	}
}

func TestRemoteWatermarkUsesOnlyPinnedBoundedDryRunAndDisposes(t *testing.T) {
	root, binding, credentials := watermarkInputs(t)
	var commands []Command
	cli := observationCLI(func(ctx context.Context, c Command) (CommandResult, error) {
		commands = append(commands, c)
		deadline, ok := ctx.Deadline()
		if !ok || time.Until(deadline) > 30*time.Second || c.Credentials != credentials || filepath.Dir(c.Directory) != root {
			t.Fatal("unbounded or unscoped dry-run")
		}
		if c.Args[0] == "version" {
			return CommandResult{Stdout: []byte(LitestreamVersion)}, nil
		}
		output := dryRunOutput(t, c)
		want := []string{"restore", "-config", filepath.Join(c.Directory, "restore.yml"), "-no-expand-env", "-o", output, "-json", "-integrity-check", "full", "-dry-run", filepath.Join(c.Directory, "remote-selector.sqlite")}
		if strings.Join(c.Args, "\x00") != strings.Join(want, "\x00") || filepath.Dir(output) != c.Directory {
			t.Fatalf("unexpected argv: %v", c.Args)
		}
		raw, err := os.ReadFile(filepath.Join(c.Directory, "restore.yml"))
		if err != nil || !strings.Contains(string(raw), `path: "epoch"`) || strings.Contains(string(raw), "PLANTED") {
			t.Fatal("config not exact or leaked credentials", err)
		}
		mustRestoreTest(t, os.WriteFile(output, []byte("unexpected dry-run output"), 0600))
		return CommandResult{Stdout: exactPlan(output, "0000000000000007")}, nil
	})
	txid, err := RemoteWatermark(context.Background(), root, cli, binding, credentials)
	if err != nil || txid != 7 || len(commands) != 2 {
		t.Fatalf("watermark=%d calls=%d err=%v", txid, len(commands), err)
	}
	assertWorkspaceEmpty(t, root)
	// A later call gets a fresh operation directory, not a reused path.
	_, err = RemoteWatermark(context.Background(), root, cli, binding, credentials)
	if err != nil || commands[0].Directory == commands[2].Directory {
		t.Fatal("workspace identity reused", err)
	}
	assertWorkspaceEmpty(t, root)
}

func TestRemoteWatermarkErrorsDisposeAndNeverMeasure(t *testing.T) {
	for _, scenario := range []string{"version", "version_truncated", "command", "truncated", "oversized", "malformed", "wrong_target", "wrong_replica", "zero", "short_txid", "uppercase", "overflow", "trailing"} {
		t.Run(scenario, func(t *testing.T) { watermarkFailure(t, scenario) })
	}
}

func watermarkFailure(t *testing.T, scenario string) {
	t.Helper()
	root, binding, credentials := watermarkInputs(t)
	calls := 0
	cli := observationCLI(func(_ context.Context, c Command) (CommandResult, error) {
		calls++
		if c.Args[0] == "version" && scenario == "version" {
			return CommandResult{Stdout: []byte("0.5.16 PLANTED_SECRET")}, nil
		}
		if c.Args[0] == "version" {
			return CommandResult{Stdout: []byte(LitestreamVersion), Truncated: scenario == "version_truncated"}, nil
		}
		output := dryRunOutput(t, c)
		mustRestoreTest(t, os.WriteFile(output, []byte("partial"), 0600))
		raw, err := failedPlan(scenario, output)
		return CommandResult{Stdout: raw, Truncated: scenario == "truncated"}, err
	})
	txid, err := RemoteWatermark(context.Background(), root, cli, binding, credentials)
	if err == nil || txid != 0 || calls > 2 || strings.Contains(err.Error(), "PLANTED") {
		t.Fatalf("unsafe failed watermark: %d %v calls=%d", txid, err, calls)
	}
	assertWorkspaceEmpty(t, root)
}

func failedPlan(scenario, output string) ([]byte, error) {
	switch scenario {
	case "command":
		return nil, errors.New("PLANTED_SECRET")
	case "oversized":
		return append(exactPlan(output, "0000000000000007"), []byte(strings.Repeat(" ", 64<<10))...), nil
	case "malformed":
		return []byte("PLANTED_SECRET"), nil
	case "wrong_target":
		return exactPlan("/foreign/file", "0000000000000007"), nil
	case "wrong_replica":
		return []byte(`{"target_path":"` + output + `","replica":"file","max_txid":"0000000000000007"}`), nil
	case "zero":
		return exactPlan(output, "0000000000000000"), nil
	case "short_txid":
		return exactPlan(output, "7"), nil
	case "uppercase":
		return exactPlan(output, "000000000000000F"), nil
	case "overflow":
		return exactPlan(output, "10000000000000000"), nil
	case "trailing":
		return append(exactPlan(output, "0000000000000007"), []byte(`{}`)...), nil
	default:
		return exactPlan(output, "0000000000000007"), nil
	}
}

func TestRemoteWatermarkCancellationDisposesPartialOutput(t *testing.T) {
	root, binding, credentials := watermarkInputs(t)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	cli := observationCLI(func(ctx context.Context, c Command) (CommandResult, error) {
		if c.Args[0] == "version" {
			return CommandResult{Stdout: []byte(LitestreamVersion)}, nil
		}
		mustRestoreTest(t, os.WriteFile(dryRunOutput(t, c), []byte("partial"), 0600))
		cancel()
		<-ctx.Done()
		return CommandResult{}, ctx.Err()
	})
	txid, err := RemoteWatermark(ctx, root, cli, binding, credentials)
	if !errors.Is(err, context.Canceled) || txid != 0 {
		t.Fatalf("cancellation lost: %d %v", txid, err)
	}
	assertWorkspaceEmpty(t, root)
}

func TestRemoteWatermarkDisposalFailurePreservesPrimaryError(t *testing.T) {
	for _, failPlan := range []bool{false, true} {
		t.Run(map[bool]string{false: "success", true: "failed_plan"}[failPlan], func(t *testing.T) { watermarkDisposalFailure(t, failPlan) })
	}
}

func watermarkDisposalFailure(t *testing.T, failPlan bool) {
	t.Helper()
	root, binding, credentials := watermarkInputs(t)
	moved := ""
	cli := observationCLI(func(_ context.Context, c Command) (CommandResult, error) {
		if c.Args[0] == "version" {
			return CommandResult{Stdout: []byte(LitestreamVersion)}, nil
		}
		output := dryRunOutput(t, c)
		moved = c.Directory + "-moved"
		mustRestoreTest(t, os.Rename(c.Directory, moved))
		mustRestoreTest(t, os.Mkdir(c.Directory, 0700))
		mustRestoreTest(t, os.WriteFile(filepath.Join(c.Directory, "keep"), []byte("replacement"), 0600))
		if failPlan {
			return CommandResult{}, errors.New("PLANTED_SECRET")
		}
		return CommandResult{Stdout: exactPlan(output, "0000000000000007")}, nil
	})
	txid, err := RemoteWatermark(context.Background(), root, cli, binding, credentials)
	if txid != 0 || err == nil || !strings.Contains(err.Error(), "workspace_changed") || strings.Contains(err.Error(), "PLANTED") {
		t.Fatalf("successful or unsafe disposal: %d %v", txid, err)
	}
	if failPlan && !strings.Contains(err.Error(), "ltx_plan_failed") {
		t.Fatal("primary error lost", err)
	}
	entries, readErr := os.ReadDir(root)
	if readErr != nil || len(entries) != 2 {
		t.Fatal("replacement was removed", entries, readErr)
	}
	mustRestoreTest(t, os.RemoveAll(moved))
	for _, entry := range entries {
		if strings.HasSuffix(entry.Name(), "-moved") {
			continue
		}
		mustRestoreTest(t, os.RemoveAll(filepath.Join(root, entry.Name())))
	}
	assertWorkspaceEmpty(t, root)
}

func TestRemoteWatermarkAdmissionHasNoCLIOrWorkspaceEffects(t *testing.T) {
	root, binding, credentials := watermarkInputs(t)
	cli := observationCLI(func(context.Context, Command) (CommandResult, error) {
		t.Fatal("invalid admission reached CLI")
		return CommandResult{}, nil
	})
	tests := []struct {
		binding     Binding
		credentials Credentials
	}{{binding, Credentials{}}, {binding, Credentials{AccessKey: credentials.AccessKey, SecretKey: credentials.SecretKey, ExpiresAt: time.Now().Add(time.Second)}}, {Binding{}, credentials}}
	for _, test := range tests {
		txid, err := RemoteWatermark(context.Background(), root, cli, test.binding, test.credentials)
		if txid != 0 || err == nil {
			t.Fatal("unsafe admission", txid, err)
		}
		assertWorkspaceEmpty(t, root)
	}
}
