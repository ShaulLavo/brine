package result

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestNativeCaptureFailureCodesAreClosedNonRetryableAndKeepCause(t *testing.T) {
	cause := errors.New("private-token private-path private-subprocess-output")
	for _, code := range []Code{NativeCaptureVerifyRefused, NativeCaptureSourceRefused, NativeCaptureLiveRefused, NativeCaptureCandidateRefused, NativeCaptureCommitRefused} {
		wrapped := New(code, cause)
		if !KnownCode(code) || Classify(wrapped).Code() != code || ExitCode(wrapped) != 1 || !errors.Is(wrapped, cause) {
			t.Fatal("capture boundary classification lost", code)
		}
		envelope := Failure("brine backup capture", wrapped)
		raw, err := json.Marshal(envelope)
		if err != nil || envelope.OK || envelope.Error.Code != code || envelope.Error.Retryable || strings.Contains(string(raw), "private-") {
			t.Fatal("capture diagnostics leaked or claimed success", code, err)
		}
		if Classify(New(code, context.Canceled)).Code() != Interrupted {
			t.Fatal("capture diagnostics hid cancellation", code)
		}
	}
}
