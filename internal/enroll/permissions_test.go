package enroll

import (
	"context"
	"errors"
	"strings"
	"testing"

	operatorpolicy "github.com/ShaulLavo/brine/internal/policy"
)

type permissionMemory struct {
	raw            []byte
	plans          map[string]permissionPlanRecord
	ops            map[string]PermissionsOperation
	writes         int
	failWriteAfter bool
	failComplete   bool
}

func (m *permissionMemory) ReadPolicy() ([]byte, error) { return m.raw, nil }
func (m *permissionMemory) WritePolicy(raw []byte) error {
	m.raw = raw
	m.writes++
	if m.failWriteAfter {
		m.failWriteAfter = false
		return errors.New("lost write acknowledgement")
	}
	return nil
}
func (m *permissionMemory) SavePlan(p permissionPlanRecord) error { m.plans[p.Plan.ID] = p; return nil }
func (m *permissionMemory) LoadPlan(id string) (permissionPlanRecord, error) {
	p, ok := m.plans[id]
	if !ok {
		return p, errors.New("missing plan")
	}
	return p, nil
}
func (m *permissionMemory) SaveOperation(op PermissionsOperation) error {
	if op.State == "succeeded" && m.failComplete {
		m.failComplete = false
		return errors.New("lost completion")
	}
	m.ops[op.ID] = op
	return nil
}
func (m *permissionMemory) LoadOperation(key string) (PermissionsOperation, bool, error) {
	op, ok := m.ops[key]
	return op, ok, nil
}
func (m *permissionMemory) PendingOperation() (string, error) {
	for key, op := range m.ops {
		if op.State == "intent" {
			return key, nil
		}
	}
	return "", nil
}

func permissionFixture(t *testing.T) (permissionService, *permissionMemory, PermissionsPlan) {
	t.Helper()
	m := &permissionMemory{raw: []byte(defaultOperatorPolicy), plans: map[string]permissionPlanRecord{}, ops: map[string]PermissionsOperation{}}
	s := permissionService{store: m, enrollment: "fixture-enrollment"}
	response, err := s.run(t.Context(), PermissionsRequest{Action: "plan", AppLabel: "demo", Changes: []operatorpolicy.PermissionAction{{Kind: operatorpolicy.AllowDomain, Domain: "demo.example"}}})
	if err != nil {
		t.Fatal(err)
	}
	return s, m, *response.Plan
}
func permissionApply(p PermissionsPlan, key string) PermissionsRequest {
	return PermissionsRequest{Action: "apply", PlanID: p.ID, IdempotencyKey: strings.Repeat(key, 32)}
}

func TestPermissionsApplyAndDuplicateDoNotReplay(t *testing.T) {
	s, m, p := permissionFixture(t)
	request := permissionApply(p, "a")
	first, err := s.run(t.Context(), request)
	if err != nil || first.Operation.State != "succeeded" {
		t.Fatal(first, err)
	}
	if _, err = s.run(t.Context(), request); err != nil || m.writes != 1 {
		t.Fatal("duplicate replay", err, m.writes)
	}
	if permissionHash(m.raw) != p.AfterHash {
		t.Fatal("wrong installed policy")
	}
	if _, err = s.run(t.Context(), PermissionsRequest{Action: "show"}); err != nil {
		t.Fatal(err)
	}
}
func TestPermissionsRecoverLostWriteAndCompletionWithoutReplay(t *testing.T) {
	for _, stage := range []string{"write", "completion"} {
		t.Run(stage, func(t *testing.T) { checkPermissionRecovery(t, stage) })
	}
}
func checkPermissionRecovery(t *testing.T, stage string) {
	t.Helper()
	s, m, p := permissionFixture(t)
	m.failWriteAfter = stage == "write"
	m.failComplete = stage == "completion"
	request := permissionApply(p, "a")
	if _, err := s.run(t.Context(), request); err == nil {
		t.Fatal("injected failure lost")
	}
	status, err := s.run(t.Context(), PermissionsRequest{Action: "status", IdempotencyKey: request.IdempotencyKey})
	if err != nil || status.Operation.State != "succeeded" {
		t.Fatal(status, err)
	}
	if _, err = s.run(t.Context(), request); err != nil || m.writes != 1 {
		t.Fatal("recovery repeated policy mutation", err, m.writes)
	}
}
func TestPermissionsStalePolicyAndReusedKeyRefused(t *testing.T) {
	s, m, p := permissionFixture(t)
	m.raw = append(m.raw, []byte("\n# independent change\n")...)
	if _, err := s.run(t.Context(), permissionApply(p, "a")); err == nil || m.writes != 0 {
		t.Fatal("stale plan changed policy")
	}
	m.raw = []byte(defaultOperatorPolicy)
	if _, err := s.run(t.Context(), permissionApply(p, "a")); err != nil {
		t.Fatal(err)
	}
	other, err := s.run(t.Context(), PermissionsRequest{Action: "plan", Changes: []operatorpolicy.PermissionAction{{Kind: operatorpolicy.AllowDomain, Domain: "other.example"}}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.run(t.Context(), permissionApply(*other.Plan, "a")); err == nil || m.writes != 1 {
		t.Fatal("idempotency key accepted another plan")
	}
}
func TestPermissionsPendingUnknownFencesAnotherPlan(t *testing.T) {
	s, m, p := permissionFixture(t)
	m.failComplete = true
	if _, err := s.run(t.Context(), permissionApply(p, "a")); err == nil {
		t.Fatal("missing injected failure")
	}
	other, err := s.run(t.Context(), PermissionsRequest{Action: "plan", Changes: []operatorpolicy.PermissionAction{{Kind: operatorpolicy.AllowDomain, Domain: "other.example"}}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.run(t.Context(), permissionApply(*other.Plan, "b")); err == nil || m.writes != 1 {
		t.Fatal("pending operation did not fence change")
	}
	m.raw = append(m.raw, []byte("\n# foreign state\n")...)
	status, err := s.run(t.Context(), PermissionsRequest{Action: "status", IdempotencyKey: strings.Repeat("a", 32)})
	if err != nil || status.Operation.State != "recovery_required" {
		t.Fatal(status, err)
	}
	if _, err = s.run(t.Context(), permissionApply(p, "a")); err == nil || m.writes != 1 {
		t.Fatal("unknown third state overwritten")
	}
}
func TestPermissionsCancellationBeforeIntentAndNoOp(t *testing.T) {
	s, m, p := permissionFixture(t)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := s.run(ctx, permissionApply(p, "a")); !errors.Is(err, context.Canceled) || len(m.ops) != 0 || m.writes != 0 {
		t.Fatal(err)
	}
	response, err := s.run(t.Context(), PermissionsRequest{Action: "plan", Changes: []operatorpolicy.PermissionAction{{Kind: operatorpolicy.RevokeDomain, Domain: "absent.example"}}})
	if err != nil || !response.Plan.NoOp {
		t.Fatal(response, err)
	}
	if _, err = s.run(t.Context(), permissionApply(*response.Plan, "b")); err != nil || m.writes != 0 {
		t.Fatal("noop rewrote policy", err)
	}
}
