//go:build linux

package jobs_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/ShaulLavo/brine/internal/jobs"
	"github.com/ShaulLavo/brine/internal/ops"
	"github.com/ShaulLavo/brine/internal/result"
	"github.com/ShaulLavo/brine/internal/store"
)

type taskHandoffCase struct {
	name     string
	kind     ops.Kind
	initial  ops.State
	conflict bool
}

type taskClaimStore struct {
	taskStore
	conflict bool
}

func (s taskClaimStore) TransitionOperation(ctx context.Context, id string, from, to ops.State) error {
	if s.conflict {
		if err := s.Store.TransitionOperation(ctx, id, from, to); err != nil {
			return err
		}
	}
	return s.Store.TransitionOperation(ctx, id, from, to)
}

func TestTaskHandlerReceivesDurablePreflight(t *testing.T) {
	cases := []taskHandoffCase{
		{name: "apply-queued", kind: ops.ModulePublishApply, initial: ops.Queued},
		{name: "apply-launch-unknown", kind: ops.ModulePublishApply, initial: ops.LaunchUnknown},
		{name: "failed-claim", kind: ops.ModulePublishApply, initial: ops.Queued, conflict: true},
	}
	for _, scenario := range cases {
		t.Run(scenario.name, func(t *testing.T) { checkTaskHandoff(t, scenario) })
	}
}

func checkTaskHandoff(t *testing.T, scenario taskHandoffCase) {
	t.Helper()
	state, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = state.Close() }()
	intent := ops.Intent{Kind: scenario.kind, App: "game", SecretRef: "sha256:" + strings.Repeat("a", 64)}
	original, _, err := state.CreateOperation(t.Context(), intent, "operator", "handoff")
	if err != nil {
		t.Fatal(err)
	}
	if scenario.initial != ops.Queued {
		if err = state.TransitionOperation(t.Context(), original.ID, ops.Queued, scenario.initial); err != nil {
			t.Fatal(err)
		}
	}
	calls := 0
	handler := func(ctx context.Context, received ops.Operation) (json.RawMessage, error) {
		calls++
		recorded, readErr := state.GetOperation(ctx, received.ID)
		if readErr != nil {
			t.Fatal(readErr)
		}
		if received.State != ops.Preflight || recorded.State != ops.Preflight {
			t.Error("handler did not receive claimed preflight", received.State, recorded.State)
		}
		if received.ID != original.ID || received.Kind != original.Kind || received.App != original.App || received.SecretRef != original.SecretRef || received.Requester != original.Requester || received.RecoveryOf != original.RecoveryOf {
			t.Error("claim changed operation identity")
		}
		return nil, result.New(result.PolicyRefused, nil)
	}
	runner := jobs.Runner{Store: taskClaimStore{taskStore: taskStore{state}, conflict: scenario.conflict}, TaskHandlers: map[ops.Kind]jobs.TaskHandler{scenario.kind: handler}}
	runErr := runner.Run(t.Context(), original.ID)
	recorded, err := state.GetOperation(t.Context(), original.ID)
	if err != nil {
		t.Fatal(err)
	}
	outcome, err := state.ReadTaskOutcome(t.Context(), original.ID)
	if err != nil {
		t.Fatal(err)
	}
	if scenario.conflict {
		if !errors.Is(runErr, ops.ErrStateConflict) || calls != 0 || recorded.State != ops.Preflight || outcome != nil {
			t.Fatal("failed claim invoked handler or changed state/outcome", calls, recorded.State, runErr)
		}
		return
	}
	if result.Classify(runErr).Code() != result.PolicyRefused || calls != 1 || recorded.State != ops.RecoveryRequired || outcome == nil || outcome.Error != result.PolicyRefused {
		t.Fatal("handler failure was changed", calls, recorded.State, runErr, outcome)
	}
	if err = runner.Run(t.Context(), original.ID); err == nil || calls != 1 {
		t.Fatal("terminal original task was replayed", err, calls)
	}
	unchanged, err := state.ReadTaskOutcome(t.Context(), original.ID)
	if err != nil || unchanged == nil || unchanged.Error != outcome.Error {
		t.Fatal("original failure outcome changed", err)
	}
}
