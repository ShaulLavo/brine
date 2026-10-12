package dispatch

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/ShaulLavo/brine/internal/jobs"
	"github.com/ShaulLavo/brine/internal/result"
)

func nativeBackupRequest(t *testing.T, operation, args string) []byte {
	t.Helper()
	raw, err := json.Marshal(Request{SchemaVersion: SchemaVersion, Op: operation, RequestID: "request-1", Args: json.RawMessage(args)})
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func nativeBackupArgs() string {
	return `{"app":"game","database":"main","idempotency_key":"` + strings.Repeat("a", 32) + `"}`
}

func nativeRestoreArgs() string {
	return `{"point_id":"` + strings.Repeat("b", 32) + `","idempotency_key":"` + strings.Repeat("a", 32) + `"}`
}

func TestNativeBackupRequestClassesAndClosedReferenceShapes(t *testing.T) {
	for _, operation := range []string{"native_capture", "native_prepare"} {
		checkNativeBackupRequest(t, operation, Mutating, nativeBackupArgs())
	}
	checkNativeBackupRequest(t, "native_restore_test", ReadOnly, nativeRestoreArgs())
}

func checkNativeBackupRequest(t *testing.T, operation string, class Class, args string) {
	t.Helper()
	actual, ok := ClassOf(operation)
	if !ok || actual != class {
		t.Fatal("native operation changed its authority class")
	}
	if _, err := DecodeRequest(nativeBackupRequest(t, operation, args)); err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"source_path", "source", "requester", "binding_id", "epoch_id", "receipt", "password", "endpoint", "sql", "wasm"} {
		invalid := strings.Replace(args, `{`, `{"`+field+`":"PLANTED_PRIVATE_VALUE",`, 1)
		if _, err := DecodeRequest(nativeBackupRequest(t, operation, invalid)); err == nil {
			t.Fatal("native request accepted authority, credentials or source content")
		}
	}
	for _, invalid := range []string{
		strings.Replace(args, `"idempotency_key":`, `"idempotency_key":null,"idempotency_key":`, 1),
		strings.Replace(args, `"idempotency_key":"`+strings.Repeat("a", 32)+`"`, `"idempotency_key":null`, 1),
		strings.Replace(args, strings.Repeat("a", 32), strings.Repeat("A", 32), 1),
		strings.Replace(args, strings.Repeat("a", 32), strings.Repeat("a", 31), 1),
		strings.Replace(args, strings.Repeat("a", 32), "../operation", 1),
		strings.Replace(args, `,"idempotency_key":"`+strings.Repeat("a", 32)+`"`, ``, 1),
	} {
		if _, err := DecodeRequest(nativeBackupRequest(t, operation, invalid)); err == nil {
			t.Fatal("native request accepted missing, null, duplicated or invalid key")
		}
	}
}

func TestNativeRequestsRefuseCrossedSelectorsBeforeFactoryOrService(t *testing.T) {
	invalid := []struct{ operation, args string }{
		{"native_capture", strings.Replace(nativeBackupArgs(), `"database":"main"`, `"database":"../main"`, 1)},
		{"native_prepare", strings.Replace(nativeBackupArgs(), `"database":"main"`, `"database":null`, 1)},
		{"native_prepare", strings.Replace(nativeBackupArgs(), `"database":"main",`, ``, 1)},
		{"native_capture", strings.Replace(nativeBackupArgs(), `"app":"game"`, `"app":"other","app":"game"`, 1)},
		{"native_capture", strings.Replace(nativeBackupArgs(), `"app":"game"`, `"App":"game"`, 1)},
		{"native_capture", nativeRestoreArgs()},
		{"native_restore_test", nativeBackupArgs()},
		{"native_restore_test", strings.Replace(nativeRestoreArgs(), strings.Repeat("b", 32), "../history", 1)},
		{"native_restore_test", strings.Replace(nativeRestoreArgs(), `"point_id":`, `"point_id":null,"point_id":`, 1)},
	}
	for _, test := range invalid {
		server := NewServer("fixture", nil)
		server.Factory = func(context.Context, string) (*Server, error) {
			t.Fatal("invalid native request opened the runtime")
			return nil, nil
		}
		_, err := server.Handle(context.Background(), bytes.NewReader(nativeBackupRequest(t, test.operation, test.args)))
		if result.Classify(err).Code() != result.DispatchInvalidRequest {
			t.Fatal("crossed selector did not refuse at the request boundary", err)
		}
	}
}

type nativeBackupsRecorder struct {
	calls   int
	kind    string
	capture NativeCaptureArgs
	prepare NativePrepareArgs
	restore NativeRestoreTestArgs
}

func (r *nativeBackupsRecorder) Capture(_ context.Context, args NativeCaptureArgs) (jobs.Accepted, error) {
	r.calls++
	r.kind, r.capture = "native_capture", args
	return jobs.Accepted{Status: "accepted", OperationID: "native-operation"}, nil
}
func (r *nativeBackupsRecorder) Prepare(_ context.Context, args NativePrepareArgs) (jobs.Accepted, error) {
	r.calls++
	r.kind, r.prepare = "native_prepare", args
	return jobs.Accepted{Status: "accepted", OperationID: "native-operation"}, nil
}
func (r *nativeBackupsRecorder) TestRestore(_ context.Context, args NativeRestoreTestArgs) (jobs.Accepted, error) {
	r.calls++
	r.kind, r.restore = "native_restore_test", args
	return jobs.Accepted{Status: "accepted", OperationID: "native-operation"}, nil
}

func TestNativeBackupDispatchRequiresAuthorizationAndConfiguredService(t *testing.T) {
	for _, operation := range []string{"native_capture", "native_prepare", "native_restore_test"} {
		checkNativeBackupDispatch(t, operation)
	}
}

func checkNativeBackupDispatch(t *testing.T, operation string) {
	t.Helper()
	args := nativeBackupArgs()
	class := Mutating
	if operation == "native_restore_test" {
		args, class = nativeRestoreArgs(), ReadOnly
	}
	recorder := &nativeBackupsRecorder{}
	server := NewServer("fixture", nil)
	server.NativeBackups = recorder
	if class == Mutating {
		_, err := server.Handle(context.Background(), bytes.NewReader(nativeBackupRequest(t, operation, args)))
		if result.Classify(err).Code() != result.DispatchOperationRefused || recorder.calls != 0 {
			t.Fatal("unauthorized capture/preparation reached its service")
		}
	}
	authorized := false
	server = server.WithJobs(nil, func(_ context.Context, observed Class) error {
		if observed != class {
			t.Fatal("native authority class changed")
		}
		authorized = true
		return nil
	})
	response, err := server.Handle(context.Background(), bytes.NewReader(nativeBackupRequest(t, operation, args)))
	if err != nil || !response.OK || !authorized || recorder.calls != 1 || recorder.kind != operation {
		t.Fatal("configured native operation did not run once after authorization", err)
	}
	if operation == "native_capture" && recorder.capture != (NativeCaptureArgs{App: "game", Database: "main", IdempotencyKey: strings.Repeat("a", 32)}) || operation == "native_prepare" && NativeCaptureArgs(recorder.prepare) != (NativeCaptureArgs{App: "game", Database: "main", IdempotencyKey: strings.Repeat("a", 32)}) || operation == "native_restore_test" && recorder.restore != (NativeRestoreTestArgs{PointID: strings.Repeat("b", 32), IdempotencyKey: strings.Repeat("a", 32)}) {
		t.Fatal("native dispatch changed reference or correlation identity")
	}
	server.NativeBackups = nil
	_, err = server.Handle(context.Background(), bytes.NewReader(nativeBackupRequest(t, operation, args)))
	if result.Classify(err).Code() != result.DependencyMissing || recorder.calls != 1 {
		t.Fatal("missing native service admitted effects or used another backend")
	}
}

func TestNativeBackupResponseAcceptsOnlyDetachedAcceptance(t *testing.T) {
	for _, operation := range []string{"native_capture", "native_prepare", "native_restore_test"} {
		checkNativeBackupResponse(t, operation)
	}
}

func checkNativeBackupResponse(t *testing.T, operation string) {
	t.Helper()
	raw, err := json.Marshal(result.Success("brine host "+operation, jobs.Accepted{Status: "accepted", OperationID: "native-operation"}))
	if err != nil {
		t.Fatal(err)
	}
	response, err := DecodeResponse(raw, operation)
	if err != nil || response.Data != (jobs.Accepted{Status: "accepted", OperationID: "native-operation"}) {
		t.Fatal("native acceptance response changed", err)
	}
	for _, invalid := range []string{
		strings.Replace(string(raw), `"status":"accepted"`, `"status":"verified"`, 1),
		strings.Replace(string(raw), `"operation_id":`, `"source":"PLANTED_PRIVATE_VALUE","operation_id":`, 1),
		strings.Replace(string(raw), `"operation_id":`, `"operation_id":null,"operation_id":`, 1),
		strings.Replace(string(raw), `"operation_id":"native-operation"`, `"operation_id":"../history"`, 1),
	} {
		if _, err := DecodeResponse([]byte(invalid), operation); result.Classify(err).Code() != result.TransportInvalidResponse {
			t.Fatal("native acceptance accepted evidence, unknown fields or invalid operation identity", err)
		}
	}
}
