package enroll

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"regexp"

	operatorpolicy "github.com/ShaulLavo/brine/internal/policy"
	"github.com/ShaulLavo/brine/internal/result"
)

type PermissionsRequest struct {
	Action         string                            `json:"action"`
	IdentityKey    []byte                            `json:"identity_key"`
	HostKey        string                            `json:"host_key"`
	PlanID         string                            `json:"plan_id,omitempty"`
	IdempotencyKey string                            `json:"idempotency_key,omitempty"`
	AppLabel       string                            `json:"app_label,omitempty"`
	Changes        []operatorpolicy.PermissionAction `json:"changes,omitempty"`
}

type PermissionsPlan struct {
	ID           string                            `json:"id"`
	EnrollmentID string                            `json:"enrollment_id"`
	BeforeHash   string                            `json:"before_hash"`
	AfterHash    string                            `json:"after_hash"`
	AppLabel     string                            `json:"app_label,omitempty"`
	Changes      []operatorpolicy.PermissionAction `json:"changes"`
	Before       operatorpolicy.Permissions        `json:"before"`
	After        operatorpolicy.Permissions        `json:"after"`
	NoOp         bool                              `json:"no_op"`
}

type permissionPlanRecord struct {
	Plan   PermissionsPlan `json:"plan"`
	Before []byte          `json:"before"`
	After  []byte          `json:"after"`
}

type PermissionsOperation struct {
	ID         string `json:"id"`
	PlanID     string `json:"plan_id"`
	State      string `json:"state"`
	BeforeHash string `json:"before_hash"`
	AfterHash  string `json:"after_hash"`
}

type PermissionsResponse struct {
	PolicyHash  string                      `json:"policy_hash,omitempty"`
	Permissions *operatorpolicy.Permissions `json:"permissions,omitempty"`
	Plan        *PermissionsPlan            `json:"plan,omitempty"`
	Operation   *PermissionsOperation       `json:"operation,omitempty"`
}

type permissionStore interface {
	ReadPolicy() ([]byte, error)
	WritePolicy([]byte) error
	SavePlan(permissionPlanRecord) error
	LoadPlan(string) (permissionPlanRecord, error)
	SaveOperation(PermissionsOperation) error
	LoadOperation(string) (PermissionsOperation, bool, error)
	PendingOperation() (string, error)
}

type permissionService struct {
	store      permissionStore
	enrollment string
}

var permissionID = regexp.MustCompile(`^[a-f0-9]{64}$`)
var permissionKey = regexp.MustCompile(`^[a-f0-9]{32}$`)

func permissionHash(raw []byte) string {
	value := sha256.Sum256(raw)
	return hex.EncodeToString(value[:])
}

func (s permissionService) run(ctx context.Context, request PermissionsRequest) (PermissionsResponse, error) {
	if err := ctx.Err(); err != nil {
		return PermissionsResponse{}, err
	}
	if err := validatePermissionsRequest(request); err != nil {
		return PermissionsResponse{}, err
	}
	switch request.Action {
	case "show":
		raw, err := s.store.ReadPolicy()
		if err != nil {
			return PermissionsResponse{}, err
		}
		parsed, err := operatorpolicy.Parse(raw)
		if err != nil {
			return PermissionsResponse{}, err
		}
		view := parsed.Permissions()
		return PermissionsResponse{PolicyHash: parsed.Hash(), Permissions: &view}, nil
	case "plan":
		return s.plan(request)
	case "apply":
		return s.apply(ctx, request)
	case "status":
		return s.status(request.IdempotencyKey)
	default:
		return PermissionsResponse{}, result.New(result.InvalidUsage, nil)
	}
}

func (s permissionService) plan(request PermissionsRequest) (PermissionsResponse, error) {
	if request.AppLabel != "" && !targetName.MatchString(request.AppLabel) {
		return PermissionsResponse{}, result.New(result.InvalidUsage, nil)
	}
	raw, err := s.store.ReadPolicy()
	if err != nil {
		return PermissionsResponse{}, err
	}
	before, err := operatorpolicy.Parse(raw)
	if err != nil {
		return PermissionsResponse{}, err
	}
	after, bytes, err := before.EditPermissions(request.Changes)
	if err != nil {
		return PermissionsResponse{}, result.New(result.InvalidUsage, err)
	}
	if before.Hash() == after.Hash() {
		bytes = raw
	}
	p := PermissionsPlan{EnrollmentID: s.enrollment, BeforeHash: permissionHash(raw), AfterHash: permissionHash(bytes), AppLabel: request.AppLabel, Changes: request.Changes, Before: before.Permissions(), After: after.Permissions(), NoOp: before.Hash() == after.Hash()}
	canonical, err := json.Marshal(p)
	if err != nil {
		return PermissionsResponse{}, err
	}
	p.ID = permissionHash(canonical)
	if err = s.store.SavePlan(permissionPlanRecord{Plan: p, Before: raw, After: bytes}); err != nil {
		return PermissionsResponse{}, err
	}
	return PermissionsResponse{Plan: &p}, nil
}

func (s permissionService) apply(ctx context.Context, request PermissionsRequest) (PermissionsResponse, error) {
	if !permissionID.MatchString(request.PlanID) || !permissionKey.MatchString(request.IdempotencyKey) {
		return PermissionsResponse{}, result.New(result.InvalidUsage, nil)
	}
	p, err := s.store.LoadPlan(request.PlanID)
	if err != nil {
		return PermissionsResponse{}, err
	}
	if p.Plan.EnrollmentID != s.enrollment || p.Plan.ID != request.PlanID || permissionHash(p.Before) != p.Plan.BeforeHash || permissionHash(p.After) != p.Plan.AfterHash {
		return PermissionsResponse{}, result.New(result.Conflict, nil)
	}
	op, exists, err := s.store.LoadOperation(request.IdempotencyKey)
	if err != nil {
		return PermissionsResponse{}, err
	}
	if exists && op.PlanID != request.PlanID {
		return PermissionsResponse{}, result.New(result.Conflict, nil)
	}
	if exists && op.State == "succeeded" {
		return PermissionsResponse{Operation: &op}, nil
	}
	pending, err := s.store.PendingOperation()
	if err != nil {
		return PermissionsResponse{}, err
	}
	if pending != "" && pending != request.IdempotencyKey {
		return PermissionsResponse{}, result.New(result.RecoveryRequired, nil)
	}
	raw, err := s.store.ReadPolicy()
	if err != nil {
		return PermissionsResponse{}, err
	}
	current := permissionHash(raw)
	if current != p.Plan.BeforeHash && (!exists || current != p.Plan.AfterHash) {
		return PermissionsResponse{}, result.New(result.Conflict, nil)
	}
	if !exists {
		op = PermissionsOperation{ID: request.IdempotencyKey, PlanID: request.PlanID, State: "intent", BeforeHash: p.Plan.BeforeHash, AfterHash: p.Plan.AfterHash}
		if err = s.store.SaveOperation(op); err != nil {
			return PermissionsResponse{}, err
		}
	}
	if err = ctx.Err(); err != nil {
		return PermissionsResponse{}, err
	}
	if current != p.Plan.AfterHash {
		if err = s.store.WritePolicy(p.After); err != nil {
			return PermissionsResponse{}, err
		}
	}
	raw, err = s.store.ReadPolicy()
	if err != nil {
		return PermissionsResponse{}, err
	}
	if permissionHash(raw) != p.Plan.AfterHash {
		return PermissionsResponse{}, result.New(result.RecoveryRequired, nil)
	}
	op.State = "succeeded"
	if err = s.store.SaveOperation(op); err != nil {
		return PermissionsResponse{}, err
	}
	return PermissionsResponse{Operation: &op}, nil
}

func (s permissionService) status(key string) (PermissionsResponse, error) {
	if !permissionKey.MatchString(key) {
		return PermissionsResponse{}, result.New(result.InvalidUsage, nil)
	}
	op, exists, err := s.store.LoadOperation(key)
	if err != nil {
		return PermissionsResponse{}, err
	}
	if !exists {
		return PermissionsResponse{}, result.New(result.Conflict, nil)
	}
	if op.State == "succeeded" {
		return PermissionsResponse{Operation: &op}, nil
	}
	raw, err := s.store.ReadPolicy()
	if err != nil {
		return PermissionsResponse{}, err
	}
	op.State = "recovery_required"
	if permissionHash(raw) == op.AfterHash {
		op.State = "succeeded"
	}
	if permissionHash(raw) == op.BeforeHash {
		op.State = "intent"
	}
	return PermissionsResponse{Operation: &op}, nil
}

var errPermissionsRefused = errors.New("operator permission request refused")

func validatePermissionsRequest(request PermissionsRequest) error {
	valid := false
	switch request.Action {
	case "show":
		valid = request.PlanID == "" && request.IdempotencyKey == "" && request.AppLabel == "" && len(request.Changes) == 0
	case "plan":
		valid = request.PlanID == "" && request.IdempotencyKey == "" && len(request.Changes) > 0
	case "apply":
		valid = permissionID.MatchString(request.PlanID) && permissionKey.MatchString(request.IdempotencyKey) && request.AppLabel == "" && len(request.Changes) == 0
	case "status":
		valid = request.PlanID == "" && permissionKey.MatchString(request.IdempotencyKey) && request.AppLabel == "" && len(request.Changes) == 0
	}
	if !valid {
		return result.New(result.InvalidUsage, nil)
	}
	return nil
}
