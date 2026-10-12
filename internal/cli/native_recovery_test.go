package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/ShaulLavo/brine/internal/dispatch"
	"github.com/ShaulLavo/brine/internal/jobs"
	"github.com/ShaulLavo/brine/internal/nativecapture"
	"github.com/ShaulLavo/brine/internal/ops"
	"github.com/ShaulLavo/brine/internal/result"
	"github.com/ShaulLavo/brine/internal/transport"
)

func TestNativeRecoveryCommandWaitAndNoWaitPreservePrivateKeyedDelivery(t *testing.T) {
	for _, noWait := range []bool{false, true} {
		t.Run(map[bool]string{false: "wait", true: "detached"}[noWait], func(t *testing.T) { checkNativeRecoveryCommand(t, noWait) })
	}
}

func checkNativeRecoveryCommand(t *testing.T, noWait bool) {
	t.Helper()
	var out, diagnostics bytes.Buffer
	deps := testDependencies(t, &out, &diagnostics)
	deps.Stdin = strings.NewReader(`{"password":"PLANTED_INDEPENDENT_RECOVERY"}`)
	deps.LoadOperationTarget = func(string, string) (transport.Target, error) { return transport.Target{Name: "fixture"}, nil }
	plan, key := "sha256:"+strings.Repeat("a", 64), strings.Repeat("b", 32)
	receipt := nativecapture.RecoveryReceipt{Kind: string(ops.NativeRecoverySet), OperationID: "recovery-operation", BindingID: strings.Repeat("c", 32), EpochID: strings.Repeat("d", 32), Reference: "recovery", Version: 1, SHA256: strings.Repeat("e", 64), Outcome: "stored"}
	submits, polls := 0, 0
	deps.OperationClient = callFunc(func(_ context.Context, _ transport.Target, request dispatch.Request) (result.Envelope, error) {
		if request.Op == "operation" {
			polls++
			return nativeRecoveryCommandStatus(t, receipt), nil
		}
		submits++
		if request.Op != string(ops.NativeRecoverySet) {
			t.Fatal("wrong private operation")
		}
		var args struct {
			Plan     string `json:"deployment_plan_id"`
			Key      string `json:"idempotency_key"`
			Password string `json:"password"`
		}
		if json.Unmarshal(request.Args, &args) != nil || args.Plan != plan || args.Key != key || args.Password != "PLANTED_INDEPENDENT_RECOVERY" {
			t.Fatal("private packet or supplied key changed")
		}
		return result.Success("brine host native_recovery_set", jobs.Accepted{Status: "accepted", OperationID: receipt.OperationID}), nil
	})
	args := []string{"backup", "recovery", "set", "--deployment-plan", plan, "--target", "fixture", "--idempotency-key", key, "--json"}
	if noWait {
		args = append(args, "--no-wait")
	}
	if err := Execute(deps, args); err != nil || submits != 1 || polls != map[bool]int{false: 1, true: 0}[noWait] {
		t.Fatal("private delivery submission/wait", err, submits, polls)
	}
	if strings.Contains(out.String()+diagnostics.String(), "PLANTED") {
		t.Fatal("private password escaped into command output")
	}
}

func nativeRecoveryCommandStatus(t *testing.T, receipt nativecapture.RecoveryReceipt) result.Envelope {
	t.Helper()
	return result.Success("brine host operation", jobs.Status{Operation: ops.Operation{ID: receipt.OperationID, Kind: ops.NativeRecoverySet, App: "game", SecretRef: "native-input", State: ops.Succeeded}, Events: []ops.Event{}, Outcome: &ops.TaskOutcome{Receipt: nativeCommandJSON(t, receipt)}})
}

func TestNativeRecoveryCommandRejectsBadPasswordBeforeTargetIO(t *testing.T) {
	var out, diagnostics bytes.Buffer
	deps := testDependencies(t, &out, &diagnostics)
	deps.Stdin = strings.NewReader(`{"password":"short"}`)
	called := false
	deps.LoadOperationTarget = func(string, string) (transport.Target, error) { called = true; return transport.Target{}, nil }
	args := []string{"backup", "recovery", "set", "--deployment-plan", "sha256:" + strings.Repeat("a", 64), "--target", "fixture", "--idempotency-key", strings.Repeat("b", 32)}
	if err := Execute(deps, args); err == nil || called {
		t.Fatal("invalid private material reached target")
	}
}
