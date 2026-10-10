package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ShaulLavo/brine/internal/apps"
	"github.com/ShaulLavo/brine/internal/dispatch"
	"github.com/ShaulLavo/brine/internal/plan"
	"github.com/ShaulLavo/brine/internal/result"
	"github.com/ShaulLavo/brine/internal/transport"
)

func TestHumanConflictDiagnosticsMatchMachinePlans(t *testing.T) {
	planID := "sha256:" + strings.Repeat("a", 64)
	conflicts := []plan.Diagnostic{{Code: plan.InsufficientDisk, Field: "free_disk_bytes"}, {Code: plan.SecretMissing, Field: "secrets.TOKEN"}}
	config := apps.ConfigPlan{PlanID: planID, Kind: plan.Conflict, Conflicts: conflicts}
	path := filepath.Join(t.TempDir(), "brine.toml")
	input, err := os.ReadFile("../spec/testdata/valid-minimal.toml")
	if err != nil {
		t.Fatal(err)
	}
	input = append(input, []byte("\n[environment]\nTOKEN = \"PLANTED_PRIVATE_ENV\"\n")...)
	if err := os.WriteFile(path, input, 0600); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name string
		args []string
		data any
	}{
		{"plan", []string{"plan", path}, dispatch.Planned{PlanID: planID, Kind: plan.Conflict, Conflicts: conflicts}},
		{"config", []string{"config", "set", "hello", "TOKEN=PLANTED_PRIVATE_ENV"}, config},
		{"restart", []string{"restart", "hello"}, config},
		{"stop", []string{"stop", "hello"}, config},
		{"start", []string{"start", "hello"}, config},
		{"remove", []string{"remove", "hello"}, config},
		{"rollback", []string{"rollback", "hello"}, apps.RollbackPlan{PlanID: planID, Kind: plan.Conflict, ReleaseID: "previous", Compatibility: "stateless_compatible", Conflicts: conflicts}},
	} {
		for _, mode := range []string{"", "--json", "--jsonl"} {
			t.Run(test.name+mode, func(t *testing.T) {
				var out, stderr bytes.Buffer
				deps := testDependencies(t, &out, &stderr)
				deps.LoadOperationTarget = func(string, string) (transport.Target, error) { return transport.Target{Name: "fixture"}, nil }
				deps.OperationClient = callFunc(func(context.Context, transport.Target, dispatch.Request) (result.Envelope, error) {
					return result.Success("fixture", test.data), nil
				})
				args := append(append([]string{}, test.args...), "--target", "fixture")
				if mode != "" {
					args = append(args, mode)
				}
				if err := Execute(deps, args); err != nil {
					t.Fatal(err)
				}
				if strings.Contains(out.String()+stderr.String(), "PLANTED_PRIVATE_ENV") || strings.Contains(out.String(), "brine apply") {
					t.Fatal("conflict leaked values or recommended apply", out.String())
				}
				if mode == "" {
					for _, conflict := range conflicts {
						if !strings.Contains(out.String(), string(conflict.Code)) || !strings.Contains(out.String(), conflict.Field) {
							t.Fatalf("missing diagnostic %+v in %s", conflict, out.String())
						}
					}
					if !strings.Contains(out.String(), "Free disk is below the required minimum.") || !strings.Contains(out.String(), "A required secret reference has no installed version.") {
						t.Fatal("missing fixed reason", out.String())
					}
				} else {
					var envelope struct {
						OK   bool `json:"ok"`
						Data struct {
							Conflicts []plan.Diagnostic `json:"conflicts"`
						} `json:"data"`
					}
					if json.Unmarshal(out.Bytes(), &envelope) != nil || !envelope.OK || len(envelope.Data.Conflicts) != len(conflicts) || strings.Count(out.String(), "\n") != 1 {
						t.Fatal("conflict must remain a successful plan", out.String())
					}
				}
			})
		}
	}
}
