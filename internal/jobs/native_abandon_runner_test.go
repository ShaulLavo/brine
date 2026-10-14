package jobs

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/ShaulLavo/brine/internal/nativecapture"
	"github.com/ShaulLavo/brine/internal/ops"
	"github.com/ShaulLavo/brine/internal/plan"
	"github.com/ShaulLavo/brine/internal/policy"
)

type abandonRunnerStore struct {
	op        ops.Operation
	outcome   *ops.TaskOutcome
	completed bool
}

func (s *abandonRunnerStore) GetOperation(context.Context, string) (ops.Operation, error) {
	return s.op, nil
}
func (s *abandonRunnerStore) ReadNativeAbandonmentOutcome(context.Context, string) (*ops.TaskOutcome, error) {
	return s.outcome, nil
}
func (s *abandonRunnerStore) CompleteTask(context.Context, string, ops.State, ops.TaskOutcome) error {
	s.completed = true
	return nil
}
func (s *abandonRunnerStore) AcquireHostLock(context.Context) (ops.Lock, error) {
	return nil, errors.New("unused")
}
func (s *abandonRunnerStore) SetOperationState(context.Context, string, ops.State) error { return nil }
func (s *abandonRunnerStore) TransitionOperation(context.Context, string, ops.State, ops.State) error {
	return nil
}
func (s *abandonRunnerStore) AcquireLaunchLock(context.Context) (ops.Lock, error) {
	return nil, errors.New("unused")
}
func (s *abandonRunnerStore) CreateOperation(context.Context, ops.Intent, string, string) (ops.Operation, bool, error) {
	return s.op, false, nil
}
func (s *abandonRunnerStore) AppendEvent(context.Context, string, ops.Event) (uint64, error) {
	return 0, nil
}
func (s *abandonRunnerStore) EventsAfter(context.Context, string, uint64, int) ([]ops.Event, error) {
	return nil, nil
}
func (s *abandonRunnerStore) LoadPlan(context.Context, string) (plan.Plan, policy.Desired, error) {
	return plan.Plan{}, policy.Desired{}, errors.New("unused")
}

func abandonRunnerOperation(state ops.State) ops.Operation {
	return ops.Operation{ID: "abandon-op", Requester: "deploy:" + strings.Repeat("a", 64), IdempotencyKey: "key", State: state, Kind: ops.NativePrepareAbandon, App: "game", SecretRef: strings.Repeat("b", 64), CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC()}
}
func abandonRunnerReceipt(op ops.Operation) json.RawMessage {
	h := strings.Repeat("c", 64)
	id := strings.Repeat("d", 32)
	raw, _ := json.Marshal(nativecapture.AbandonReceipt{Kind: string(ops.NativePrepareAbandon), OperationID: op.ID, InputSHA256: op.SecretRef, PointID: id, PointSHA256: h, PreparationOperationID: "prepare", FenceOwnerOperationID: "prepare", FenceID: id, BindingID: id, EpochID: id, SourceSealSHA256: h, CurrentStopSHA256: h, CurrentCodeSHA256: h, CurrentRoutesSHA256: h, ReleaseID: "release", StoppedDesiredRevision: 2, CompletedAt: time.Now().UTC(), Outcome: "abandoned_stopped"})
	return raw
}
func TestNativeAbandonRunnerAdoptsCommittedSuccessAfterAckLoss(t *testing.T) {
	op := abandonRunnerOperation(ops.Succeeded)
	receipt := abandonRunnerReceipt(op)
	s := &abandonRunnerStore{op: op, outcome: &ops.TaskOutcome{Receipt: receipt}}
	r := Runner{Store: s}
	err := r.finishNativeAbandonTask(context.Background(), op, nil, errors.New("ack lost"), s)
	if err == nil || !strings.Contains(err.Error(), "ack lost") || s.completed {
		t.Fatal("ack loss was rewritten", err, s.completed)
	}
}
func TestNativeAbandonRunnerMarksPrecommitFailureRecovery(t *testing.T) {
	op := abandonRunnerOperation(ops.Preflight)
	s := &abandonRunnerStore{op: op}
	r := Runner{Store: s}
	if err := r.finishNativeAbandonTask(context.Background(), op, nil, errors.New("handler failed"), s); err == nil || !s.completed {
		t.Fatal("precommit failure not recovered", err, s.completed)
	}
}
func TestNativeAbandonRunnerRejectsReturnedReceiptMismatch(t *testing.T) {
	op := abandonRunnerOperation(ops.Succeeded)
	stored := abandonRunnerReceipt(op)
	s := &abandonRunnerStore{op: op, outcome: &ops.TaskOutcome{Receipt: stored}}
	r := Runner{Store: s}
	if err := r.finishNativeAbandonTask(context.Background(), op, json.RawMessage(`{"bad":true}`), nil, s); err == nil || s.completed {
		t.Fatal("mismatched receipt accepted", err)
	}
}
