package cli

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/datainit"
	"github.com/ShaulLavo/brine/internal/dispatch"
	"github.com/ShaulLavo/brine/internal/result"
	"github.com/ShaulLavo/brine/internal/transport"
)

func TestDataInitializationClientCarriesOnlyPlanReferences(t *testing.T) {
	var out, stderr bytes.Buffer
	deps := testDependencies(t, &out, &stderr)
	hash := "sha256:" + strings.Repeat("a", 64)
	calls := 0
	deps.LoadOperationTarget = func(string, string) (transport.Target, error) { return transport.Target{Name: "fixture"}, nil }
	deps.OperationClient = callFunc(func(_ context.Context, _ transport.Target, r dispatch.Request) (result.Envelope, error) {
		calls++
		if r.Op != "data_init_apply" || string(r.Args) != `{"app":"hello","plan_id":"`+hash+`"}` {
			t.Fatalf("unexpected request %s %s", r.Op, r.Args)
		}
		return result.Success("brine host "+r.Op, datainit.Operation{ID: strings.Repeat("1", 32), PlanID: hash, Fence: data.FenceID(strings.Repeat("2", 32)), State: "succeeded"}), nil
	})
	if err := Execute(deps, []string{"data", "init", "hello", "--plan-id", hash, "--target", "fixture", "--json"}); err != nil || calls != 1 {
		t.Fatal(err, calls)
	}
	if !strings.Contains(out.String(), "succeeded") {
		t.Fatal("result not printed")
	}
}

func TestDataInitializationRejectsInvalidUsageBeforeRuntime(t *testing.T) {
	hash := "sha256:" + strings.Repeat("a", 64)
	for _, args := range [][]string{
		{"host", "data-init", "hello", "--plan"},
		{"host", "data-init", "hello", "--plan-id", hash, "--artifact", hash},
		{"data", "init", "hello", "--plan", "--first-release-plan", hash, "--artifact", hash, "--approved"},
		{"data", "init", "hello", "--plan", "--first-release-plan", hash, "--artifact", hash, "--restore-point", hash},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			var out, stderr bytes.Buffer
			deps := testDependencies(t, &out, &stderr)
			err := ExecuteWithRuntime(deps, args, RuntimeLifecycle{Open: func(context.Context, bool) (RuntimeServices, error) {
				t.Fatal("invalid command opened host runtime")
				return RuntimeServices{}, nil
			}})
			if err == nil {
				t.Fatal("invalid initialization accepted")
			}
		})
	}
}

func TestDataInitializationOperatorCannotBeSelectedByRemoteRequest(t *testing.T) {
	for _, item := range []struct {
		args     []string
		operator bool
	}{
		{[]string{"host", "data-init", "hello"}, true},
		{[]string{"data", "init", "hello"}, false},
		{[]string{"host", "serve"}, false},
		{[]string{"data", "init", "hello", "--operator"}, false},
	} {
		if got := HostDataInitializationRequested(context.Background(), item.args); got != item.operator {
			t.Fatalf("args=%v operator=%v", item.args, got)
		}
	}
}
