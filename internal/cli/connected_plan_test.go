package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ShaulLavo/brine/internal/dispatch"
	"github.com/ShaulLavo/brine/internal/plan"
	"github.com/ShaulLavo/brine/internal/result"
	"github.com/ShaulLavo/brine/internal/transport"
)

type connectedPlanClient struct {
	t     *testing.T
	calls int
}

func (c *connectedPlanClient) Call(_ context.Context, _ transport.Target, r dispatch.Request) (result.Envelope, error) {
	c.calls++
	if r.Op != "plan" {
		c.t.Fatal("not connected planning")
	}
	var args dispatch.PlanArgs
	if json.Unmarshal(r.Args, &args) != nil || !strings.Contains(args.Spec, "container_port = 3000") {
		c.t.Fatal("spec not sent")
	}
	return result.Success("brine host plan", dispatch.Planned{PlanID: "sha256:" + strings.Repeat("a", 64), Kind: plan.Create, Conflicts: []plan.Diagnostic{}}), nil
}
func TestConnectedPlanClient(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "brine.toml")
	raw, err := os.ReadFile("../spec/testdata/valid-minimal.toml")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	client := &connectedPlanClient{t: t}
	deps := Dependencies{Context: context.Background(), Stdin: strings.NewReader(""), Stdout: io.Discard, Stderr: io.Discard, Version: "fixture", OperationClient: client, LoadOperationTarget: func(string, string) (transport.Target, error) { return transport.Target{}, nil }}
	if err := Execute(deps, []string{"plan", path, "--target", "fixture", "--json"}); err != nil {
		t.Fatal(err)
	}
	if client.calls != 1 {
		t.Fatal("plan did not call host")
	}
}

func TestConnectedPlanInvalidSpec(t *testing.T) {
	valid, err := os.ReadFile("../spec/testdata/valid-minimal.toml")
	if err != nil {
		t.Fatal(err)
	}
	for name, input := range map[string]string{
		"malformed":     "name = \"PLANTED_PRIVATE_SPEC\"\nimage = [",
		"missing-field": "name = \"PLANTED_PRIVATE_SPEC\"\n",
		"unknown-field": string(valid) + "\nPLANTED_PRIVATE_SPEC = \"hidden\"\n",
	} {
		for _, mode := range []string{"", "--json", "--jsonl"} {
			t.Run(name+mode, func(t *testing.T) {
				path := filepath.Join(t.TempDir(), "brine.toml")
				if err := os.WriteFile(path, []byte(input), 0600); err != nil {
					t.Fatal(err)
				}
				var out, stderr bytes.Buffer
				deps := testDependencies(t, &out, &stderr)
				deps.LoadOperationTarget = func(string, string) (transport.Target, error) {
					t.Fatal("invalid spec loaded target")
					return transport.Target{}, nil
				}
				deps.OperationClient = callFunc(func(context.Context, transport.Target, dispatch.Request) (result.Envelope, error) {
					t.Fatal("invalid spec contacted target")
					return result.Envelope{}, nil
				})
				args := []string{"plan", path, "--target", "fixture"}
				if mode != "" {
					args = append(args, mode)
				}
				err := Execute(deps, args)
				if result.ExitCode(err) != 2 || result.Classify(err).Code() != result.InvalidUsage {
					t.Fatalf("err=%v exit=%d", err, result.ExitCode(err))
				}
				if strings.Contains(out.String()+stderr.String(), "PLANTED_PRIVATE_SPEC") {
					t.Fatal("private parse input leaked")
				}
				if mode != "" {
					var e result.Envelope
					if json.Unmarshal(out.Bytes(), &e) != nil || e.OK || e.Error == nil || e.Error.Code != result.InvalidUsage || strings.Count(out.String(), "\n") != 1 {
						t.Fatalf("invalid envelope %s", out.String())
					}
				}
			})
		}
	}
}

func TestPlanMissingModeGuidanceDoesNotDenyConnectedPlanning(t *testing.T) {
	for _, mode := range []string{"", "--json", "--jsonl"} {
		t.Run(mode, func(t *testing.T) {
			var out, stderr bytes.Buffer
			deps := testDependencies(t, &out, &stderr)
			deps.LoadOperationTarget = func(string, string) (transport.Target, error) {
				t.Fatal("missing target loaded configuration")
				return transport.Target{}, nil
			}
			args := []string{"plan", "PLANTED_PRIVATE_UNREAD_FILE"}
			if mode != "" {
				args = append(args, mode)
			}
			err := Execute(deps, args)
			if result.ExitCode(err) != 2 || result.Classify(err).Code() != result.OfflineRequired {
				t.Fatalf("unexpected missing-mode error %v", err)
			}
			text := out.String() + stderr.String()
			if strings.Contains(text, "Connected planning is not available") || !strings.Contains(text, "--target NAME") || strings.Contains(text, "PLANTED_PRIVATE_UNREAD_FILE") {
				t.Fatalf("misleading or unsafe planning guidance %s", text)
			}
		})
	}
}
