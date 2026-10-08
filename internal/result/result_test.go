package result

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"testing"
)

func TestExitCategories(t *testing.T) {
	for _, tt := range []struct {
		code     Code
		category Category
		exit     int
	}{
		{InternalError, Operational, 1}, {InvalidUsage, Validation, 2}, {DependencyMissing, Dependency, 3},
		{PolicyRefused, Policy, 4}, {Conflict, StateConflict, 5}, {RecoveryRequired, Recovery, 6}, {Interrupted, Interruption, 130}, {TUIInteractive, Validation, 2},
	} {
		t.Run(string(tt.code), func(t *testing.T) {
			cause := errors.New("unsafe subprocess output")
			err := New(tt.code, cause)
			if err.Category() != tt.category || ExitCode(fmt.Errorf("wrapped: %w", err)) != tt.exit {
				t.Fatalf("wrong category/exit for %s", tt.code)
			}
			if !errors.Is(err, cause) {
				t.Fatal("lost cause")
			}
			if err.Error() == cause.Error() {
				t.Fatal("unsafe message")
			}
		})
	}
	if ExitCode(nil) != 0 || ExitCode(errors.New("unknown")) != 1 || ExitCode(fmt.Errorf("wrapped: %w", context.Canceled)) != 130 {
		t.Fatal("fallback mapping")
	}
	if ExitCode(New(InternalError, context.Canceled)) != 130 {
		t.Fatal("cancellation must override classification")
	}
}

func TestZeroAndUnknownErrorsAreOperational(t *testing.T) {
	for _, err := range []*Error{{}, New(Code("unknown"), nil)} {
		if ExitCode(err) != 1 || err.Code() != InternalError {
			t.Fatalf("invalid error = %+v", err)
		}
	}
}

func TestDocumentedErrorCodes(t *testing.T) {
	contract, err := os.ReadFile("../../docs/CONTRACTS.md")
	if err != nil {
		t.Fatal(err)
	}
	for code, d := range descriptions {
		row := fmt.Sprintf("| `%s` | %d | %t | %s |", code, d.category, d.retryable, d.message)
		if !strings.Contains(string(contract), row) {
			t.Fatalf("contract missing exact row %q", row)
		}
		response := Failure("brine", New(code, errors.New("synthetic-secret")))
		if response.OK || response.Data != nil || response.Error.Code != code || response.Error.Message != d.message || response.Error.Retryable != d.retryable {
			t.Fatalf("error envelope = %+v", response)
		}
	}
	if !strings.Contains(string(contract), "| `schema_version` | integer | Always `"+strconv.Itoa(SchemaVersion)+"` for this contract. |") {
		t.Fatal("schema version drift")
	}
}
