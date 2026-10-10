package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ShaulLavo/brine/internal/dispatch"
	"github.com/ShaulLavo/brine/internal/plan"
	"github.com/ShaulLavo/brine/internal/result"
	"github.com/ShaulLavo/brine/internal/transport"
)

func TestDataPrepareCLIRequestsPlanOnly(t *testing.T) {
	raw, err := os.ReadFile("../spec/testdata/valid-minimal.toml")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "brine.toml")
	if err = os.WriteFile(path, raw, 0600); err != nil { //nolint:gosec // Path is a fixed-name fixture inside a private test directory.
		t.Fatal(err)
	}
	for _, mode := range []string{"", "--json", "--jsonl"} {
		t.Run(mode, func(t *testing.T) {
			var out, diagnostics bytes.Buffer
			deps := testDependencies(t, &out, &diagnostics)
			calls := 0
			deps.LoadOperationTarget = func(string, string) (transport.Target, error) { return transport.Target{}, nil }
			deps.OperationClient = callFunc(func(_ context.Context, _ transport.Target, request dispatch.Request) (result.Envelope, error) {
				calls++
				if request.Op != "data_prepare_plan" {
					t.Fatal("preparation command executed mutation", request.Op)
				}
				var args dispatch.PlanArgs
				if json.Unmarshal(request.Args, &args) != nil || args.Spec != string(raw) {
					t.Fatal("spec altered")
				}
				return result.Success("brine host data_prepare_plan", dispatch.Planned{PlanID: "sha256:" + strings.Repeat("a", 64), Kind: plan.Create, Conflicts: []plan.Diagnostic{}}), nil
			})
			args := []string{"data", "prepare", path, "--target", "fixture"}
			if mode != "" {
				args = append(args, mode)
			}
			if err := Execute(deps, args); err != nil || calls != 1 {
				t.Fatalf("prepare: %v calls=%d output=%s", err, calls, out.String())
			}
			if mode != "" {
				var envelope result.Envelope
				if json.Unmarshal(out.Bytes(), &envelope) != nil || !envelope.OK {
					t.Fatal("invalid machine response", out.String())
				}
			}
		})
	}
}
