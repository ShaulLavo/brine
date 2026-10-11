package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/ShaulLavo/brine/internal/archive"
	"github.com/ShaulLavo/brine/internal/data"
)

func TestArchiveCommandRejectsPathsAndInvalidPlansBeforeRuntime(t *testing.T) {
	hash := "sha256:" + strings.Repeat("a", 64)
	for _, args := range [][]string{{"host", "archive-reconcile", "/private/data"}, {"host", "archive-reconcile", strings.Repeat("a", 32), "--path", "/private/data"}, {"host", "archive-retire", "/private/data"}, {"host", "archive-abandon", "hello"}, {"host", "archive-abandon", strings.Repeat("a", 32), "--path", "/private/data"}, {"host", "archive-prepare", "hello"}, {"host", "archive-prepare", "hello", "--plan-id", "bad"}, {"host", "archive-prepare", "hello", "--plan", "--plan-id", hash}, {"host", "archive-prepare", "hello", "--plan", "--archive-path", "/private/data"}, {"host", "archive-prepare", "hello", "--plan", "--requester", "local-operator"}} {
		var stdout, stderr bytes.Buffer
		deps := testDependencies(t, &stdout, &stderr)
		if err := ExecuteWithRuntime(deps, args, RuntimeLifecycle{Open: func(context.Context, bool) (RuntimeServices, error) {
			t.Fatal("invalid command opened host runtime")
			return RuntimeServices{}, nil
		}}); err == nil {
			t.Fatal("invalid command admitted", args)
		}
	}
}
func TestArchiveCommandIsNotRemoteDispatch(t *testing.T) {
	for _, test := range []struct {
		args    []string
		archive bool
	}{{[]string{"host", "archive-prepare", "hello", "--plan"}, true}, {[]string{"host", "archive-retire", strings.Repeat("a", 32)}, true}, {[]string{"host", "archive-abandon", strings.Repeat("a", 32)}, true}, {[]string{"host", "archive-reconcile", strings.Repeat("a", 32), "--inspect"}, true}, {[]string{"host", "serve"}, false}, {[]string{"app", "remove", "hello"}, false}} {
		if got := HostArchiveRequested(context.Background(), test.args); got != test.archive {
			t.Fatal("incorrect archive selection", test.args, got)
		}
	}
}

type recoveryCommands struct {
	ArchiveOperations
	inspectCalls, reconcileCalls int
	id                           data.ArchiveID
	err                          error
}

func (r *recoveryCommands) Inspect(_ context.Context, id data.ArchiveID) (archive.Inspection, error) {
	r.inspectCalls++
	r.id = id
	return archive.Inspection{Disposition: archive.Disposition{ArchiveID: id, Stage: archive.RenameIntent}, CanReconcile: true}, r.err
}
func (r *recoveryCommands) Reconcile(_ context.Context, id data.ArchiveID) (archive.Disposition, error) {
	r.reconcileCalls++
	r.id = id
	return archive.Disposition{ArchiveID: id, Stage: archive.Retired}, r.err
}
func TestArchiveRecoveryCommandSelectsInspectAndMutationByImmutableID(t *testing.T) {
	id := data.ArchiveID(strings.Repeat("a", 32))
	for _, inspect := range []bool{false, true} {
		for _, failure := range []error{nil, archive.ErrRecovery, archive.ErrRefused} {
			var stdout, stderr bytes.Buffer
			deps := testDependencies(t, &stdout, &stderr)
			fake := &recoveryCommands{err: failure}
			deps.HostArchives = fake
			root := NewRootCommand(deps)
			args := []string{"host", "archive-reconcile", string(id)}
			if inspect {
				args = append(args, "--inspect")
			}
			root.SetArgs(args)
			err := root.ExecuteContext(context.Background())
			if (err != nil) != (failure != nil) || fake.id != id || fake.inspectCalls+fake.reconcileCalls != 1 || (fake.inspectCalls == 1) != inspect {
				t.Fatal("incorrect recovery routing", inspect, failure, err, fake)
			}
			if failure == nil {
				var out map[string]any
				if err = json.Unmarshal(stdout.Bytes(), &out); err != nil || strings.Contains(stdout.String(), "\x1b") || !strings.Contains(stdout.String(), string(id)) {
					t.Fatal("invalid machine recovery result", stdout.String(), err)
				}
			}
		}
	}
}
