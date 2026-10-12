package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/dispatch"
	"github.com/ShaulLavo/brine/internal/jobs"
	"github.com/ShaulLavo/brine/internal/nativecapture"
	"github.com/ShaulLavo/brine/internal/ops"
	"github.com/ShaulLavo/brine/internal/result"
	"github.com/ShaulLavo/brine/internal/transport"
)

func nativeCommandArgs(kind ops.Kind) []string {
	args := []string{"backup", "capture", "game", "--database", "main", "--target", "fixture"}
	if kind == ops.NativePrepare {
		args[1] = "prepare"
	}
	if kind == ops.NativeRestoreTest {
		args = []string{"restore", "native-test", strings.Repeat("b", 32), "--target", "fixture"}
	}
	return args
}

func nativeCommandReceipt(kind ops.Kind) nativecapture.TaskReceipt {
	return nativecapture.TaskReceipt{Kind: string(kind), OperationID: "native-operation", BindingID: strings.Repeat("c", 32), EpochID: strings.Repeat("d", 32), PointID: strings.Repeat("b", 32), PointSHA256: strings.Repeat("e", 64), Outcome: "verified"}
}

func nativeCommandStatus(kind ops.Kind, raw json.RawMessage) result.Envelope {
	return result.Success("brine host operation", jobs.Status{Operation: ops.Operation{ID: "native-operation", Kind: kind, App: "game", SecretRef: "native-input", State: ops.Succeeded}, Events: []ops.Event{}, Outcome: &ops.TaskOutcome{Receipt: raw}})
}

func nativeCommandJSON(t *testing.T, value any) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func checkNativeCommandInput(t *testing.T, kind ops.Kind, request dispatch.Request, key string) {
	t.Helper()
	if request.Op != string(kind) {
		t.Fatal("native command changed operation kind")
	}
	decoded, err := dispatch.DecodeRequest(nativeCommandJSON(t, request))
	if err != nil {
		t.Fatal("CLI emitted invalid native request", err)
	}
	var input struct {
		App            string `json:"app"`
		Database       string `json:"database"`
		PointID        string `json:"point_id"`
		IdempotencyKey string `json:"idempotency_key"`
	}
	if json.Unmarshal(decoded.Args, &input) != nil || !data.ValidID(input.IdempotencyKey) || key != "" && input.IdempotencyKey != key {
		t.Fatal("native command lost its exact request key")
	}
	if kind == ops.NativeRestoreTest {
		if input.PointID != strings.Repeat("b", 32) || input.App != "" || input.Database != "" {
			t.Fatal("native restore test supplied source authority instead of a point")
		}
		return
	}
	if input.App != "game" || input.Database != "main" || input.PointID != "" {
		t.Fatal("native backup changed app/database selection")
	}
}

func checkNativeCommandPoll(t *testing.T, request dispatch.Request, operationID string) {
	t.Helper()
	var input dispatch.OperationArgs
	if json.Unmarshal(request.Args, &input) != nil || input.OperationID != operationID {
		t.Fatal("native command polled another operation")
	}
}

func TestNativeCommandsWaitForTheExactTaggedReceipt(t *testing.T) {
	for _, kind := range []ops.Kind{ops.NativeCapture, ops.NativePrepare, ops.NativeRestoreTest} {
		checkNativeCommandModes(t, kind)
	}
}

func checkNativeCommandModes(t *testing.T, kind ops.Kind) {
	t.Helper()
	for _, mode := range []string{"", "--json", "--jsonl"} {
		checkNativeCommandReceipt(t, kind, mode)
	}
}

func checkNativeCommandReceipt(t *testing.T, kind ops.Kind, mode string) {
	t.Helper()
	var out, diagnostics bytes.Buffer
	deps := testDependencies(t, &out, &diagnostics)
	deps.LoadOperationTarget = func(string, string) (transport.Target, error) { return transport.Target{Name: "fixture"}, nil }
	key := strings.Repeat("a", 32)
	receipt := nativeCommandReceipt(kind)
	submitted, polls := 0, 0
	deps.OperationClient = callFunc(func(_ context.Context, _ transport.Target, request dispatch.Request) (result.Envelope, error) {
		if request.Op == "operation" {
			polls++
			checkNativeCommandPoll(t, request, receipt.OperationID)
			return nativeCommandStatus(kind, nativeCommandJSON(t, receipt)), nil
		}
		submitted++
		checkNativeCommandInput(t, kind, request, key)
		return result.Success("brine host "+string(kind), jobs.Accepted{Status: "accepted", OperationID: receipt.OperationID}), nil
	})
	args := append(nativeCommandArgs(kind), "--idempotency-key", key)
	if mode != "" {
		args = append(args, mode)
	}
	if err := Execute(deps, args); err != nil || submitted != 1 || polls != 1 {
		t.Fatal("native command did not wait on the accepted task", err, submitted, polls)
	}
	if !strings.Contains(diagnostics.String(), "operation_id=native-operation") || strings.Contains(diagnostics.String(), "idempotency_key=") {
		t.Fatal("native command lost acceptance identity or regenerated a supplied key")
	}
	if mode == "" {
		if !strings.Contains(out.String(), receipt.PointID) || !strings.Contains(out.String(), "verified") || kind == ops.NativePrepare && !strings.Contains(out.String(), "fence remains held") {
			t.Fatal("human native result lost point or preparation fence state")
		}
		return
	}
	var envelope struct {
		Data nativecapture.TaskReceipt `json:"data"`
	}
	if json.Unmarshal(out.Bytes(), &envelope) != nil || envelope.Data != receipt || strings.Count(out.String(), "\n") != 1 {
		t.Fatal("machine native result changed the closed tagged receipt")
	}
}

func TestNativeCommandNoWaitReportsGeneratedKeyAndNeverPolls(t *testing.T) {
	for _, kind := range []ops.Kind{ops.NativeCapture, ops.NativePrepare, ops.NativeRestoreTest} {
		checkNativeCommandNoWait(t, kind)
	}
}

func checkNativeCommandNoWait(t *testing.T, kind ops.Kind) {
	t.Helper()
	var out, diagnostics bytes.Buffer
	deps := testDependencies(t, &out, &diagnostics)
	deps.LoadOperationTarget = func(string, string) (transport.Target, error) { return transport.Target{Name: "fixture"}, nil }
	key := ""
	calls := 0
	deps.OperationClient = callFunc(func(_ context.Context, _ transport.Target, request dispatch.Request) (result.Envelope, error) {
		calls++
		checkNativeCommandInput(t, kind, request, "")
		var input struct {
			Key string `json:"idempotency_key"`
		}
		if json.Unmarshal(request.Args, &input) != nil {
			t.Fatal("missing generated native key")
		}
		key = input.Key
		return result.Success("brine host "+string(kind), jobs.Accepted{Status: "accepted", OperationID: "native-operation"}), nil
	})
	args := append(nativeCommandArgs(kind), "--no-wait", "--json")
	if err := Execute(deps, args); err != nil || calls != 1 {
		t.Fatal("native no-wait polled or repeated work", err, calls)
	}
	if !data.ValidID(key) || diagnostics.String() != "idempotency_key="+key+"\n" || !strings.Contains(out.String(), `"status":"accepted"`) || strings.Contains(out.String(), "verified") {
		t.Fatal("native no-wait hid recovery key or claimed verification")
	}
}

func TestNativeCommandSelectorsRefuseBeforeTargetIO(t *testing.T) {
	invalid := [][]string{
		{"backup", "capture", "game", "--target", "fixture"},
		{"backup", "capture", "../game", "--database", "main", "--target", "fixture"},
		{"backup", "prepare", "game", "--database", "../main", "--target", "fixture"},
		{"backup", "capture", "game", "--database", "main", "--target", "../fixture"},
		append(nativeCommandArgs(ops.NativeCapture), "--idempotency-key", ""),
		append(nativeCommandArgs(ops.NativePrepare), "--idempotency-key", strings.Repeat("A", 32)),
		append(nativeCommandArgs(ops.NativeRestoreTest), "--idempotency-key", "../operation"),
		{"restore", "native-test", "../history", "--target", "fixture"},
		append(nativeCommandArgs(ops.NativeCapture), "--source", "PLANTED_PRIVATE_PATH"),
	}
	for _, args := range invalid {
		var out, diagnostics bytes.Buffer
		deps := testDependencies(t, &out, &diagnostics)
		deps.LoadOperationTarget = func(string, string) (transport.Target, error) {
			t.Fatal("invalid native selector did target IO")
			return transport.Target{}, nil
		}
		if err := Execute(deps, args); result.Classify(err).Code() != result.InvalidUsage {
			t.Fatal("invalid native selector did not refuse", err)
		}
	}
}

func TestNativeCommandsRejectCrossedReceiptsAndRecoveryRequired(t *testing.T) {
	for _, variant := range []string{"kind", "operation", "point", "private", "recovery"} {
		checkNativeCommandRefusedReceipt(t, variant)
	}
}

func checkNativeCommandRefusedReceipt(t *testing.T, variant string) {
	t.Helper()
	var out, diagnostics bytes.Buffer
	deps := testDependencies(t, &out, &diagnostics)
	deps.LoadOperationTarget = func(string, string) (transport.Target, error) { return transport.Target{Name: "fixture"}, nil }
	receipt := nativeCommandReceipt(ops.NativeRestoreTest)
	if variant == "kind" {
		receipt.Kind = string(ops.NativeCapture)
	}
	if variant == "operation" {
		receipt.OperationID = "foreign-operation"
	}
	if variant == "point" {
		receipt.PointID = strings.Repeat("f", 32)
	}
	raw := nativeCommandJSON(t, receipt)
	if variant == "private" {
		raw = json.RawMessage(strings.Replace(string(raw), `{`, `{"database_path":"PLANTED_PRIVATE_PATH",`, 1))
	}
	submitted, polls := 0, 0
	deps.OperationClient = callFunc(func(_ context.Context, _ transport.Target, request dispatch.Request) (result.Envelope, error) {
		if request.Op != "operation" {
			submitted++
			return result.Success("brine host native_restore_test", jobs.Accepted{Status: "accepted", OperationID: "native-operation"}), nil
		}
		polls++
		if variant == "recovery" {
			return result.Success("brine host operation", jobs.Status{Operation: ops.Operation{ID: "native-operation", Kind: ops.NativeRestoreTest, State: ops.RecoveryRequired}}), nil
		}
		return nativeCommandStatus(ops.NativeRestoreTest, raw), nil
	})
	args := append(nativeCommandArgs(ops.NativeRestoreTest), "--idempotency-key", strings.Repeat("a", 32), "--json")
	err := Execute(deps, args)
	want := result.TransportInvalidResponse
	if variant == "recovery" {
		want = result.RecoveryRequired
	}
	if result.Classify(err).Code() != want || submitted != 1 || polls != 1 || strings.Contains(out.String()+diagnostics.String(), "PLANTED_PRIVATE_PATH") {
		t.Fatal("crossed receipt or unknown effect gained a success or retry", err, submitted, polls)
	}
}
