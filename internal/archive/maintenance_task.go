package archive

import (
	"encoding/json"
	"time"

	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/strictjson"
)

type MaintenanceReceipt struct {
	Kind                string          `json:"kind"`
	OperationID         string          `json:"operation_id"`
	OriginalOperationID string          `json:"original_operation_id"`
	PlanID              string          `json:"plan_id"`
	ArchiveID           data.ArchiveID  `json:"archive_id"`
	MaintenanceKind     MaintenanceKind `json:"maintenance_kind"`
	Outcome             string          `json:"outcome"`
	EvidenceSHA256      string          `json:"evidence_sha256"`
	CompletedAt         time.Time       `json:"completed_at"`
}

func (r MaintenanceReceipt) Valid() bool {
	if !nativeArchiveOperationID.MatchString(r.OperationID) || !digest.MatchString(r.PlanID) || !data.ValidID(string(r.ArchiveID)) || !digest.MatchString(r.EvidenceSHA256) || r.CompletedAt.IsZero() || r.CompletedAt.Location() != time.UTC {
		return false
	}
	if r.Kind == "archive_maintenance_apply" && r.OriginalOperationID != "" || r.Kind == "archive_maintenance_resolve" && (!nativeArchiveOperationID.MatchString(r.OriginalOperationID) || r.OriginalOperationID == r.OperationID) {
		return false
	}
	if r.Kind != "archive_maintenance_apply" && r.Kind != "archive_maintenance_resolve" {
		return false
	}
	if r.MaintenanceKind == VerifyRetention {
		return r.Kind == "archive_maintenance_apply" && r.Outcome == "verified"
	}
	return (r.MaintenanceKind == PurgeArchive || r.MaintenanceKind == ExpireArchive) && r.Outcome == "local_deleted"
}
func DecodeMaintenanceReceipt(raw []byte) (MaintenanceReceipt, error) {
	if len(raw) == 0 || len(raw) > 4096 {
		return MaintenanceReceipt{}, ErrRefused
	}
	if _, err := strictjson.Object(raw, "kind", "operation_id", "original_operation_id", "plan_id", "archive_id", "maintenance_kind", "outcome", "evidence_sha256", "completed_at"); err != nil {
		return MaintenanceReceipt{}, ErrRefused
	}
	var r MaintenanceReceipt
	if json.Unmarshal(raw, &r) != nil || !r.Valid() {
		return MaintenanceReceipt{}, ErrRefused
	}
	return r, nil
}
