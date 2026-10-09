package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/ShaulLavo/brine/internal/apps"
	"github.com/ShaulLavo/brine/internal/dispatch"
	"github.com/ShaulLavo/brine/internal/plan"
	"github.com/ShaulLavo/brine/internal/result"
	"github.com/ShaulLavo/brine/internal/transport"
)

func TestRemoveHumanAndMachineGoldens(t *testing.T) {
	for _, mode := range []string{"human", "json", "jsonl"} {
		t.Run(mode, func(t *testing.T) {
			var out, stderr bytes.Buffer
			deps := testDependencies(t, &out, &stderr)
			deps.LoadOperationTarget = func(string, string) (transport.Target, error) { return transport.Target{Name: "fixture"}, nil }
			deps.OperationClient = callFunc(func(_ context.Context, _ transport.Target, request dispatch.Request) (result.Envelope, error) {
				var args dispatch.LifecycleArgs
				if json.Unmarshal(request.Args, &args) != nil || request.Op != "lifecycle" || args.App != "hello" || args.Action != plan.RemoveApp {
					t.Fatal(request)
				}
				return result.Success("brine host lifecycle", apps.ConfigPlan{PlanID: "sha256:" + strings.Repeat("a", 64), Kind: plan.Update, Lifecycle: plan.RemoveApp, SecretRetention: "d5_retained_releases", Conflicts: []plan.Diagnostic{}}), nil
			})
			args := []string{"remove", "hello", "--target", "fixture"}
			file := "remove.txt"
			if mode != "human" {
				args = append(args, "--"+mode)
				file = "remove.json"
			}
			if err := Execute(deps, args); err != nil {
				t.Fatal(err)
			}
			want, err := os.ReadFile("testdata/" + file)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(out.Bytes(), want) {
				t.Fatalf("output mismatch\ngot %s\nwant %s", out.Bytes(), want)
			}
		})
	}
}
