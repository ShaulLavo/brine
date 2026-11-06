package cli

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/ShaulLavo/brine/internal/dispatch"
	"github.com/ShaulLavo/brine/internal/jobs"
	"github.com/ShaulLavo/brine/internal/ops"
	"github.com/ShaulLavo/brine/internal/result"
	"github.com/ShaulLavo/brine/internal/transport"
)

func TestWaitAcceptedTaskRejectsMalformedPage(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status jobs.Status
	}{
		{"empty advances cursor", jobs.Status{NextCursor: 1}},
		{"sequence at cursor", jobs.Status{Events: []ops.Event{{Sequence: 0}}}},
		{"duplicate sequence", jobs.Status{Events: []ops.Event{{Sequence: 1}, {Sequence: 1}}, NextCursor: 1}},
		{"cursor differs from last event", jobs.Status{Events: []ops.Event{{Sequence: 1}}, NextCursor: 2}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			checkMalformedTaskPage(t, tc.status)
		})
	}
}

func checkMalformedTaskPage(t *testing.T, page jobs.Status) {
	t.Helper()
	status := page
	status.Operation = ops.Operation{ID: "op1", Kind: ops.RestoreTest, State: ops.Failed}
	status.Outcome = &ops.TaskOutcome{Error: result.PolicyRefused}
	accepted := jobs.Accepted{Status: "accepted", OperationID: "op1"}
	_, err := waitAcceptedTask(context.Background(), accepted, ops.RestoreTest, false, func(context.Context, dispatch.OperationArgs) (jobs.Status, error) { return status, nil })
	var failure *acceptedTaskFailure
	if result.Classify(err).Code() != result.TransportInvalidResponse || !errors.As(err, &failure) || failure.accepted != accepted {
		t.Fatalf("malformed page accepted or locator lost: %v", err)
	}
}

func TestLaunchUnknownStatusGuidance(t *testing.T) {
	for _, mode := range []string{"", "--json", "--jsonl"} {
		t.Run(mode, func(t *testing.T) { checkLaunchUnknownStatusGuidance(t, mode) })
	}
}

func checkLaunchUnknownStatusGuidance(t *testing.T, mode string) {
	t.Helper()
	var out, stderr bytes.Buffer
	deps := testDependencies(t, &out, &stderr)
	deps.LoadOperationTarget = func(string, string) (transport.Target, error) { return transport.Target{Name: "fixture"}, nil }
	calls := 0
	status := jobs.Status{Operation: ops.Operation{ID: "op1", Kind: ops.Deploy, State: ops.LaunchUnknown}, Events: []ops.Event{}}
	deps.OperationClient = callFunc(func(_ context.Context, _ transport.Target, request dispatch.Request) (result.Envelope, error) {
		calls++
		if request.Op != "operation" {
			t.Fatal("status mutated host")
		}
		return result.Success("fixture", status), nil
	})
	dir := "/fixture/space ' $(printf UNQUOTED)"
	args := []string{"status", "--operation", "op1", "--target", "fixture", "--config-dir", dir}
	if mode != "" {
		args = append(args, mode)
	}
	if err := Execute(deps, args); err != nil || calls != 1 || status.Operation.State.IsTerminal() {
		t.Fatalf("uncertain status = %v, calls = %d", err, calls)
	}
	if mode != "" {
		if strings.Contains(out.String(), "reconcile") || strings.Count(out.String(), "\n") != 1 {
			t.Fatalf("machine output changed: %s", out.String())
		}
		return
	}
	_, command, ok := strings.Cut(out.String(), "Inspect launch uncertainty with: ")
	if !ok || !strings.Contains(out.String(), "not known") {
		t.Fatalf("no explicit uncertainty guidance: %s", out.String())
	}
	got := shellArguments(t, strings.TrimSuffix(command, "\n"))
	want := []string{"brine", "reconcile", "--dry-run", "--target", "fixture", "--config-dir", dir}
	if strings.Join(got, "\x00") != strings.Join(want, "\x00") {
		t.Fatalf("guidance = %q, want %q", got, want)
	}
}

type operationPageCase struct {
	name      string
	id        string
	cursor    uint64
	sequences []uint64
	next      uint64
	valid     bool
}

func TestOperationPageValidation(t *testing.T) {
	for _, tc := range []operationPageCase{
		{"matching page", "op1", 2, []uint64{3, 5}, 5, true},
		{"empty preserves cursor", "op1", 2, nil, 2, true},
		{"wrong ID", "other", 2, []uint64{3}, 3, false},
		{"duplicate sequence", "op1", 2, []uint64{3, 3}, 3, false},
		{"descending sequence", "op1", 2, []uint64{4, 3}, 3, false},
		{"sequence at cursor", "op1", 2, []uint64{2}, 2, false},
		{"sequence below cursor", "op1", 2, []uint64{1}, 1, false},
		{"cursor ahead of events", "op1", 2, []uint64{3}, 4, false},
		{"cursor behind events", "op1", 2, []uint64{3}, 2, false},
		{"empty advances cursor", "op1", 2, nil, 3, false},
		{"empty regresses cursor", "op1", 2, nil, 1, false},
	} {
		t.Run(tc.name, func(t *testing.T) { checkOperationPageCase(t, tc) })
	}
}

func checkOperationPageCase(t *testing.T, tc operationPageCase) {
	t.Helper()
	status := jobs.Status{Operation: ops.Operation{ID: tc.id}, NextCursor: tc.next}
	for _, sequence := range tc.sequences {
		status.Events = append(status.Events, ops.Event{Sequence: sequence})
	}
	err := checkOperationPage("op1", tc.cursor, status)
	if (err == nil) != tc.valid || err != nil && result.Classify(err).Code() != result.TransportInvalidResponse {
		t.Fatalf("validation = %v, valid = %t", err, tc.valid)
	}
	var out, stderr bytes.Buffer
	deps := testDependencies(t, &out, &stderr)
	deps.LoadOperationTarget = func(string, string) (transport.Target, error) { return transport.Target{Name: "fixture"}, nil }
	deps.OperationClient = callFunc(func(_ context.Context, _ transport.Target, request dispatch.Request) (result.Envelope, error) {
		if request.Op != "operation" || string(request.Args) != `{"operation_id":"op1","after_cursor":2}` {
			t.Fatalf("wrong page request: %+v", request)
		}
		return result.Success("fixture", status), nil
	})
	err = Execute(deps, []string{"status", "--operation", "op1", "--after-cursor", "2", "--target", "fixture", "--json"})
	if (err == nil) != tc.valid || err != nil && result.Classify(err).Code() != result.TransportInvalidResponse {
		t.Fatalf("CLI validation = %v, valid = %t", err, tc.valid)
	}
}
