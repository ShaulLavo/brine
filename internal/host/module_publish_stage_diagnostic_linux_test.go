//go:build linux

package host

import (
	"bytes"
	"context"
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
