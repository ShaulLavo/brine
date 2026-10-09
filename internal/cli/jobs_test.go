package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ShaulLavo/brine/internal/dispatch"
	"github.com/ShaulLavo/brine/internal/jobs"
	"github.com/ShaulLavo/brine/internal/ops"
	"github.com/ShaulLavo/brine/internal/result"
	"github.com/ShaulLavo/brine/internal/transport"
)

type callFunc func(context.Context, transport.Target, dispatch.Request) (result.Envelope, error)

func (f callFunc) Call(c context.Context, t transport.Target, r dispatch.Request) (result.Envelope, error) {
	return f(c, t, r)
}

type opRunFunc func(context.Context, string) error

func (f opRunFunc) Run(c context.Context, id string) error { return f(c, id) }

func TestApplyAndOperationCLI(t *testing.T) {
	for _, machine := range []string{"", "--json", "--jsonl"} {
		for _, op := range []string{"apply", "operation"} {
			t.Run(op+machine, func(t *testing.T) {
				dir := t.TempDir()
				target := transport.Target{Name: "fixture", Destination: "runner@fixture", IdentityPath: "/fixture/key", PinnedHostKey: "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"}
				raw, _ := json.Marshal(target)
				if err := os.WriteFile(filepath.Join(dir, "fixture.json"), raw, 0600); err != nil {
					t.Fatal(err)
				}
				var out, stderr bytes.Buffer
				deps := testDependencies(t, &out, &stderr)
				calls := 0
				deps.OperationClient = callFunc(func(ctx context.Context, got transport.Target, req dispatch.Request) (result.Envelope, error) {
					calls++
					if got != target || req.Op != op {
						t.Fatalf("target or request: %+v %+v", got, req)
					}
					if _, err := dispatch.EncodeRequest(req); err != nil {
						t.Fatal(err)
					}
					if op == "apply" {
						var a dispatch.ApplyArgs
						_ = json.Unmarshal(req.Args, &a)
						if a.PlanID != "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" || a.IdempotencyKey != "retry-key" {
							t.Fatalf("args %+v", a)
						}
						return result.Success("brine host apply", jobs.Accepted{Status: "accepted", OperationID: "op1"}), nil
					}
					var a dispatch.OperationArgs
					_ = json.Unmarshal(req.Args, &a)
					if a.OperationID != "op1" || a.AfterCursor != 42 {
						t.Fatalf("args %+v", a)
					}
					return result.Success("brine host operation", jobs.Status{Operation: ops.Operation{ID: "op1", PlanID: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", State: ops.Succeeded}, Events: []ops.Event{}, NextCursor: 42}), nil
				})
				args := []string{"apply", "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "--idempotency-key", "retry-key"}
				if op == "operation" {
					args = []string{"status", "--operation", "op1", "--after-cursor", "42"}
				}
				args = append(args, "--target", "fixture", "--config-dir", dir)
				if machine != "" {
					args = append(args, machine)
				}
				if err := Execute(deps, args); err != nil {
					t.Fatalf("%v stderr=%s", err, stderr.String())
				}
				if calls != 1 || strings.Contains(out.String(), "deployed") {
					t.Fatalf("calls %d output %s", calls, out.String())
				}
				if machine != "" {
					var e result.Envelope
					if err := json.Unmarshal(out.Bytes(), &e); err != nil || !e.OK || e.Command == "brine host "+op || strings.Count(out.String(), "\n") != 1 {
						t.Fatalf("bad envelope %s %v", out.String(), err)
					}
				} else if !strings.Contains(out.String(), "op1") {
					t.Fatalf("no operation ID %q", out.String())
				}
			})
		}
	}
}

func TestObserverCancellationDoesNotSendMutation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	var out, stderr bytes.Buffer
	deps := testDependencies(t, &out, &stderr)
	deps.Context = ctx
	deps.LoadOperationTarget = func(string, string) (transport.Target, error) { return transport.Target{Name: "fixture"}, nil }
	calls := 0
	deps.OperationClient = callFunc(func(ctx context.Context, _ transport.Target, r dispatch.Request) (result.Envelope, error) {
		calls++
		if r.Op != "operation" {
			t.Fatal("observer mutated host")
		}
		cancel()
		return result.Envelope{}, ctx.Err()
	})
	err := Execute(deps, []string{"status", "--operation", "op1", "--target", "fixture", "--json"})
	if !errors.Is(err, context.Canceled) || calls != 1 || !strings.Contains(out.String(), `"code":"interrupted"`) {
		t.Fatalf("err=%v calls=%d out=%s", err, calls, out.String())
	}
}

func TestHostRunOpBoundary(t *testing.T) {
	for _, tc := range []struct {
		uid     int
		id      string
		wantRun bool
	}{{0, "op1", false}, {1234, "../escape", false}, {1234, "op1", true}} {
		var out, stderr bytes.Buffer
		deps := testDependencies(t, &out, &stderr)
		deps.HostUID = func() int { return tc.uid }
		ran := false
		deps.HostOperationRunner = opRunFunc(func(_ context.Context, id string) error {
			ran = true
			if id != tc.id {
				t.Fatal(id)
			}
			return nil
		})
		err := Execute(deps, []string{"host", "run-op", tc.id})
		if ran != tc.wantRun || (err == nil) != tc.wantRun {
			t.Fatalf("uid=%d id=%s ran=%v err=%v", tc.uid, tc.id, ran, err)
		}
	}
}

func TestJobCLIMissingOrUnsafeIDs(t *testing.T) {
	for _, args := range [][]string{{"apply", "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}, {"apply", "../x", "--target", "fixture"}, {"status", "--operation", "", "--target", "fixture"}, {"status", "--operation", "op1", "--target", "../fixture"}} {
		var out, stderr bytes.Buffer
		deps := testDependencies(t, &out, &stderr)
		deps.LoadOperationTarget = func(string, string) (transport.Target, error) {
			t.Fatal("invalid input performed IO")
			return transport.Target{}, nil
		}
		err := Execute(deps, append(args, "--json"))
		if err == nil || result.ExitCode(err) != 2 {
			t.Fatalf("args=%v err=%v", args, err)
		}
	}
}
