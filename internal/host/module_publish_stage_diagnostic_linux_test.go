//go:build linux

package host

import (
	"bytes"
	"context"
	"errors"
	"github.com/ShaulLavo/brine/internal/datapublish"
	"github.com/ShaulLavo/brine/internal/localexec"
	"strings"
	"testing"
	"time"
)

func TestModuleStageDiagnosticsAreClosedAndNeverOwnerProof(t *testing.T) {
	var output bytes.Buffer
	if RunModuleStage(context.Background(), strings.NewReader("PRIVATE_PUBLISHER_SIGNATURE"), &output) == nil {
		t.Fatal("ordinary host ran contained helper")
	}
	if code := decodeModuleStageDiagnostic(output.Bytes()); code != "guest_containment" {
		t.Fatal("missing bounded containment refusal", code)
	}
	if bytes.Contains(output.Bytes(), []byte("PRIVATE_PUBLISHER_SIGNATURE")) {
		t.Fatal("input leaked into diagnostics")
	}
	if _, err := decodeModuleStageObservation(output.Bytes(), time.Time{}, time.Time{}); err == nil {
		t.Fatal("refusal became owner proof")
	}
	for _, raw := range []string{`{"format":"brine-module-stage-refusal-v1","code":"PRIVATE_SECRET"}`, `{"format":"brine-module-stage-refusal-v1","code":"guest_listener","details":"PRIVATE_SECRET"}`, `{"format":"brine-module-stage-refusal-v1","code":"guest_listener","code":"guest_publisher"}`} {
		if decodeModuleStageDiagnostic([]byte(raw)) != "" {
			t.Fatal("untrusted diagnostics admitted")
		}
	}
}

func TestModuleStageCleanupKeepsFirstFailurePhase(t *testing.T) {
	for _, phase := range []string{"guest_child_bind", "guest_listener", "guest_publisher", "guest_operation"} {
		if got := moduleStageTerminalCode(phase, datapublish.ErrRecord, datapublish.ErrRecord); got != phase {
			t.Fatal("cleanup replaced first failure", phase, got)
		}
	}
	if got := moduleStageTerminalCode("guest_operation", nil, datapublish.ErrRecord); got != "guest_child_stop" {
		t.Fatal("sole stop failure was lost", got)
	}
}

func TestModuleStageAttachSuccessPreservesClosedGuestRefusal(t *testing.T) {
	var output bytes.Buffer
	if err := emitModuleStageDiagnostic(&output, "guest_child_bind"); err != nil {
		t.Fatal(err)
	}
	err := moduleStageRunFailure(nil, output.Bytes())
	if err == nil || !errors.Is(err, datapublish.ErrRecord) {
		t.Fatal("attach exit0 concealed guest exit1")
	}
	lease := &publicationStageLease{initialStage: &nativeInitialStageWork{failureCode: "input_attach"}}
	primary := initialPublisherFailureCode(lease, err, output.Bytes())
	lease.initialStage.failureCode = "helper_stop"
	setInitialPublisherFailure(lease, err, datapublish.ErrRecord, primary)
	if lease.initialStage.failureCode != "guest_child_bind" {
		t.Fatal("cleanup replaced guest refusal")
	}
	if _, err := decodeModuleStageObservation(output.Bytes(), time.Now().UTC(), time.Now().UTC()); err == nil {
		t.Fatal("refusal became initial owner")
	}
	typed := &localexec.Error{Kind: localexec.UnknownOutcome}
	wrapped := moduleStageRunFailure(typed, output.Bytes())
	if !errors.Is(wrapped, typed) {
		t.Fatal("guest diagnosis replaced typed command failure")
	}
	raw := []byte(`{"format":"brine-module-stage-refusal-v1","code":"guest_child_bind","secret":"PRIVATE_VALUE"}`)
	if moduleStageRunFailure(nil, raw) != nil {
		t.Fatal("untrusted output became closed diagnosis")
	}
	for _, attack := range []string{string(raw), `{"format":"brine-module-stage-refusal-v1","code":"PRIVATE_CODE"}`, `{"format":"brine-module-stage-refusal-v1","code":"guest_child_bind","code":"guest_input"}`, `malformed PRIVATE_VALUE`} {
		failure := moduleStageRunFailure(typed, []byte(attack))
		if !errors.Is(failure, typed) || moduleStageFailureCode(failure, "fallback") != "fallback" {
			t.Fatal("untrusted stdout replaced primary failure or supplied a code")
		}
	}
}
