package ops

import (
	"encoding/json"
	"testing"

	"github.com/ShaulLavo/brine/internal/result"
)

func TestNativeCaptureBoundaryOutcomesAreStrictTerminalFailures(t *testing.T) {
	for _, kind := range []Kind{NativeCapture, NativeBackupResolve} {
		op := Operation{Kind: kind, State: RecoveryRequired}
		for _, code := range []result.Code{result.NativeCaptureVerifyRefused, result.NativeCaptureSourceRefused, result.NativeCaptureLiveRefused, result.NativeCaptureCandidateRefused, result.NativeCaptureCommitRefused} {
			outcome := TaskOutcome{Error: code}
			raw, err := json.Marshal(outcome)
			if err != nil {
				t.Fatal(err)
			}
			decoded, err := DecodeTaskOutcome(op, raw)
			if err != nil || decoded.Error != code {
				t.Fatal("closed failure outcome refused", kind, code, err)
			}
			success := op
			success.State = Succeeded
			if outcome.Valid(success) {
				t.Fatal("capture refusal accepted as successful outcome", code)
			}
			if _, err = DecodeTaskOutcome(op, []byte(`{"error":"`+string(code)+`","details":"private"}`)); err == nil {
				t.Fatal("open diagnostic payload admitted", code)
			}
		}
		if _, err := DecodeTaskOutcome(op, []byte(`{"error":"arbitrary_failure_text"}`)); err == nil {
			t.Fatal("arbitrary failure text admitted")
		}
	}
}
