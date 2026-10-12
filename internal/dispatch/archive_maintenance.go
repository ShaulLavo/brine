package dispatch

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/ShaulLavo/brine/internal/archive"
	"github.com/ShaulLavo/brine/internal/backupcredentials"
	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/jobs"
	"github.com/ShaulLavo/brine/internal/result"
	"github.com/ShaulLavo/brine/internal/strictjson"
)

type ArchiveMaintenancePlanArgs struct {
	ArchiveID data.ArchiveID          `json:"archive_id"`
	Kind      archive.MaintenanceKind `json:"kind"`
}
type ArchiveMaintenanceApplyArgs struct {
	ArchiveID      data.ArchiveID `json:"archive_id"`
	PlanID         string         `json:"plan_id"`
	IdempotencyKey string         `json:"idempotency_key"`
}
type ArchiveMaintenanceResolveArgs struct {
	OperationID    string `json:"operation_id"`
	IdempotencyKey string `json:"idempotency_key"`
}
type ArchiveMaintenanceStatusArgs struct {
	ArchiveID data.ArchiveID `json:"archive_id"`
}
type ArchiveCredentialPlanArgs struct {
	ArchiveID string     `json:"archive_id"`
	Binding   string     `json:"binding_id"`
	Epoch     string     `json:"epoch_id"`
	ExpiresAt *time.Time `json:"expires_at"`
}
type ArchiveCredentialSetArgs struct {
	ArchiveID, Binding, Epoch, PlanID string
	Packet                            backupcredentials.Packet
}

func (a ArchiveCredentialSetArgs) Clear()                     { a.Packet.Clear() }
func (ArchiveCredentialSetArgs) String() string               { return "[private archived credential delivery]" }
func (ArchiveCredentialSetArgs) GoString() string             { return "[private archived credential delivery]" }
func (ArchiveCredentialSetArgs) MarshalJSON() ([]byte, error) { return []byte(`{"private":true}`), nil }

type ArchiveMaintenanceOperations interface {
	PlanMaintenance(context.Context, ArchiveMaintenancePlanArgs) (archive.MaintenancePlan, error)
	ApplyMaintenance(context.Context, ArchiveMaintenanceApplyArgs) (jobs.Accepted, error)
	ResolveMaintenance(context.Context, ArchiveMaintenanceResolveArgs) (jobs.Accepted, error)
	ArchiveStatus(context.Context, ArchiveMaintenanceStatusArgs) (archive.MaintenanceStatus, error)
	PlanArchiveCredentials(context.Context, ArchiveCredentialPlanArgs) (backupcredentials.ArchivePlan, error)
	SetArchiveCredentials(context.Context, ArchiveCredentialSetArgs) (backupcredentials.ArchiveReceipt, error)
}

func decodeArchiveMaintenancePlan(raw json.RawMessage) (any, error) {
	if _, err := strictjson.Object(raw, "archive_id", "kind"); err != nil {
		return nil, strictjson.ErrObject
	}
	var a ArchiveMaintenancePlanArgs
	if json.Unmarshal(raw, &a) != nil || !data.ValidID(string(a.ArchiveID)) || a.Kind != archive.VerifyRetention && a.Kind != archive.PurgeArchive && a.Kind != archive.ExpireArchive {
		return nil, strictjson.ErrObject
	}
	return a, nil
}
func decodeArchiveMaintenanceApply(raw json.RawMessage) (any, error) {
	if _, err := strictjson.Object(raw, "archive_id", "plan_id", "idempotency_key"); err != nil {
		return nil, strictjson.ErrObject
	}
	var a ArchiveMaintenanceApplyArgs
	if json.Unmarshal(raw, &a) != nil || !data.ValidID(string(a.ArchiveID)) || !jobs.ValidPlanID(a.PlanID) || !data.ValidID(a.IdempotencyKey) {
		return nil, strictjson.ErrObject
	}
	return a, nil
}
func decodeArchiveMaintenanceResolve(raw json.RawMessage) (any, error) {
	if _, err := strictjson.Object(raw, "operation_id", "idempotency_key"); err != nil {
		return nil, strictjson.ErrObject
	}
	var a ArchiveMaintenanceResolveArgs
	if json.Unmarshal(raw, &a) != nil || !jobs.ValidID(a.OperationID) || !data.ValidID(a.IdempotencyKey) {
		return nil, strictjson.ErrObject
	}
	return a, nil
}
func decodeArchiveMaintenanceStatus(raw json.RawMessage) (any, error) {
	if _, err := strictjson.Object(raw, "archive_id"); err != nil {
		return nil, strictjson.ErrObject
	}
	var a ArchiveMaintenanceStatusArgs
	if json.Unmarshal(raw, &a) != nil || !data.ValidID(string(a.ArchiveID)) {
		return nil, strictjson.ErrObject
	}
	return a, nil
}
func decodeArchiveCredentialPlan(raw json.RawMessage) (any, error) {
	fields, err := strictjson.Object(raw, "archive_id", "binding_id", "epoch_id", "expires_at")
	if err != nil {
		return nil, strictjson.ErrObject
	}
	var a ArchiveCredentialPlanArgs
	if json.Unmarshal(raw, &a) != nil || !archiveCredentialScopeIDs(a.ArchiveID, a.Binding, a.Epoch) {
		return nil, strictjson.ErrObject
	}
	if string(fields["expires_at"]) != "null" {
		expiry, err := strictjson.Value[time.Time](fields["expires_at"])
		if err != nil || expiry.IsZero() || expiry.Location() != time.UTC {
			return nil, strictjson.ErrObject
		}
	}
	return a, nil
}
func archiveCredentialScopeIDs(archiveID, binding, epoch string) bool {
	return data.ValidID(archiveID) && data.ValidID(binding) && data.ValidID(epoch)
}
func decodeArchiveCredentialSet(raw json.RawMessage) (any, error) {
	fields, err := strictjson.Object(raw, "archive_id", "binding_id", "epoch_id", "plan_id", "packet")
	if err != nil {
		return nil, strictjson.ErrObject
	}
	archiveID, aErr := strictjson.Value[string](fields["archive_id"])
	binding, bErr := strictjson.Value[string](fields["binding_id"])
	epoch, eErr := strictjson.Value[string](fields["epoch_id"])
	plan, pErr := strictjson.Value[string](fields["plan_id"])
	if aErr != nil || bErr != nil || eErr != nil || pErr != nil || !archiveCredentialScopeIDs(archiveID, binding, epoch) || !backupcredentials.ValidPlanID(plan) {
		return nil, strictjson.ErrObject
	}
	packet, err := backupcredentials.DecodePacket(fields["packet"])
	if err != nil {
		return nil, strictjson.ErrObject
	}
	return ArchiveCredentialSetArgs{ArchiveID: archiveID, Binding: binding, Epoch: epoch, PlanID: plan, Packet: packet}, nil
}
func EncodeArchiveCredentialSet(archiveID, binding, epoch, plan string, packet []byte) (json.RawMessage, error) {
	raw, err := json.Marshal(struct {
		ArchiveID string          `json:"archive_id"`
		Binding   string          `json:"binding_id"`
		Epoch     string          `json:"epoch_id"`
		Plan      string          `json:"plan_id"`
		Packet    json.RawMessage `json:"packet"`
	}{archiveID, binding, epoch, plan, packet})
	if err != nil {
		return nil, strictjson.ErrObject
	}
	checked, err := decodeArchiveCredentialSet(raw)
	if err != nil {
		clear(raw)
		return nil, err
	}
	checked.(ArchiveCredentialSetArgs).Clear()
	return raw, nil
}

func (s *Server) archiveMaintenance(ctx context.Context, args any) (any, error) {
	if s.ArchiveMaintenance == nil {
		return nil, result.New(result.DependencyMissing, nil)
	}
	switch a := args.(type) {
	case ArchiveMaintenancePlanArgs:
		return s.ArchiveMaintenance.PlanMaintenance(ctx, a)
	case ArchiveMaintenanceApplyArgs:
		return s.ArchiveMaintenance.ApplyMaintenance(ctx, a)
	case ArchiveMaintenanceResolveArgs:
		return s.ArchiveMaintenance.ResolveMaintenance(ctx, a)
	case ArchiveMaintenanceStatusArgs:
		return s.ArchiveMaintenance.ArchiveStatus(ctx, a)
	case ArchiveCredentialPlanArgs:
		return s.ArchiveMaintenance.PlanArchiveCredentials(ctx, a)
	case ArchiveCredentialSetArgs:
		return s.ArchiveMaintenance.SetArchiveCredentials(ctx, a)
	default:
		return nil, result.New(result.DispatchInvalidRequest, nil)
	}
}
func archiveMaintenanceError(err error) error {
	var typed *result.Error
	if errors.As(err, &typed) {
		return result.Classify(err)
	}
	if errors.Is(err, archive.ErrRecovery) || errors.Is(err, backupcredentials.ErrStorage) {
		return result.New(result.RecoveryRequired, nil)
	}
	if errors.Is(err, backupcredentials.ErrInvalid) {
		return result.New(result.InvalidUsage, nil)
	}
	if errors.Is(err, backupcredentials.ErrStale) {
		return result.New(result.Conflict, nil)
	}
	return result.New(result.PolicyRefused, nil)
}
