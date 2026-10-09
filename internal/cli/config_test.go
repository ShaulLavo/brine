package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/ShaulLavo/brine/internal/apps"
	"github.com/ShaulLavo/brine/internal/dispatch"
	"github.com/ShaulLavo/brine/internal/plan"
	"github.com/ShaulLavo/brine/internal/result"
	"github.com/ShaulLavo/brine/internal/secrets"
	"github.com/ShaulLavo/brine/internal/transport"
)

func TestConfigAndLifecycleClient(t *testing.T) {
	for _, mode := range []string{"", "--json", "--jsonl"} {
		for _, command := range []string{"config", "restart", "stop", "start", "remove"} {
			t.Run(command+mode, func(t *testing.T) {
				var out, stderr bytes.Buffer
				deps := testDependencies(t, &out, &stderr)
				deps.LoadOperationTarget = func(string, string) (transport.Target, error) { return transport.Target{Name: "fixture"}, nil }
				calls := 0
				deps.OperationClient = callFunc(func(_ context.Context, _ transport.Target, req dispatch.Request) (result.Envelope, error) {
					calls++
					if command == "config" {
						var a dispatch.ConfigArgs
						json.Unmarshal(req.Args, &a)
						if a.App != "hello" || len(a.Edits) != 3 || a.Edits[0].Key != "environment.API_KEY" || a.Edits[0].Value != "PLANTED_PRIVATE_SETTING" {
							t.Fatal("wrong edit request")
						}
					} else if req.Op != "lifecycle" {
						t.Fatal(req.Op)
					}
					return result.Success("brine host "+req.Op, apps.ConfigPlan{PlanID: "sha256:" + strings.Repeat("a", 64), Kind: plan.Update, Conflicts: []plan.Diagnostic{}}), nil
				})
				args := []string{command, "hello", "--target", "fixture"}
				if command == "config" {
					args = []string{"config", "set", "hello", "API_KEY=PLANTED_PRIVATE_SETTING", "resources.memory_mb=256", "--unset", "environment.OLD_KEY", "--target", "fixture"}
				}
				if mode != "" {
					args = append(args, mode)
				}
				if err := Execute(deps, args); err != nil || calls != 1 {
					t.Fatal(err, calls)
				}
				if strings.Contains(out.String()+stderr.String(), "PLANTED_PRIVATE_SETTING") {
					t.Fatal("value in output")
				}
			})
		}
	}
}
func TestSecretClientStdinOnly(t *testing.T) {
	var out, stderr bytes.Buffer
	deps := testDependencies(t, &out, &stderr)
	deps.Stdin = strings.NewReader("PLANTED_PRIVATE_SECRET")
	deps.LoadOperationTarget = func(string, string) (transport.Target, error) { return transport.Target{Name: "fixture"}, nil }
	deps.OperationClient = callFunc(func(_ context.Context, _ transport.Target, r dispatch.Request) (result.Envelope, error) {
		var a dispatch.SecretArgs
		if json.Unmarshal(r.Args, &a) != nil || string(a.Value) != "PLANTED_PRIVATE_SECRET" {
			t.Fatal("stdin value missing")
		}
		return result.Success("brine host secret_set", secrets.Stored{OperationID: "operation1", VersionName: "brine.hello.token.v1"}), nil
	})
	if err := Execute(deps, []string{"secret", "set", "hello", "token", "--target", "fixture", "--json"}); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String()+stderr.String(), "PLANTED_PRIVATE_SECRET") {
		t.Fatal("value leaked")
	}
	for _, args := range [][]string{{"secret", "set", "hello", "token", "PLANTED_PRIVATE_SECRET", "--target", "fixture"}, {"secret", "set", "hello", "token", "--value", "PLANTED_PRIVATE_SECRET", "--target", "fixture"}} {
		out.Reset()
		stderr.Reset()
		if result.ExitCode(Execute(deps, args)) != 2 {
			t.Fatal("accepted argv secret")
		}
		if strings.Contains(out.String()+stderr.String(), "PLANTED_PRIVATE_SECRET") {
			t.Fatal("usage leaked")
		}
	}
}
func TestSecretInputBound(t *testing.T) {
	for _, input := range []string{"", strings.Repeat("x", secrets.ValueLimit+1)} {
		var out, stderr bytes.Buffer
		deps := testDependencies(t, &out, &stderr)
		deps.Stdin = strings.NewReader(input)
		deps.LoadOperationTarget = func(string, string) (transport.Target, error) {
			t.Fatal("invalid value performed IO")
			return transport.Target{}, nil
		}
		if err := Execute(deps, []string{"secret", "set", "hello", "token", "--target", "fixture"}); err == nil {
			t.Fatal("accepted invalid input")
		}
	}
}
