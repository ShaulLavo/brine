package dispatch

import (
	"bytes"
	"encoding/json"
	"time"

	"github.com/ShaulLavo/brine/internal/archive"
	"github.com/ShaulLavo/brine/internal/backupcredentials"
	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/strictjson"
)

func archiveClosedValue[T any](raw []byte, required, optional []string) (T, error) {
	var value T
	var names map[string]json.RawMessage
	if json.Unmarshal(raw, &names) != nil {
		return value, strictjson.ErrObject
	}
	allowed := map[string]bool{}
	for _, key := range required {
		allowed[key] = true
	}
	for _, key := range optional {
		allowed[key] = true
	}
	fields := make([]string, 0, len(names))
	for key, v := range names {
		if !allowed[key] || bytes.Equal(bytes.TrimSpace(v), []byte("null")) {
			return value, strictjson.ErrObject
		}
		fields = append(fields, key)
	}
	for _, key := range required {
		if names[key] == nil {
			return value, strictjson.ErrObject
		}
	}
	if _, err := strictjson.Object(raw, fields...); err != nil {
		return value, strictjson.ErrObject
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&value) != nil {
		return value, strictjson.ErrObject
	}
	return value, nil
}
func decodeArchiveMaintenancePlanResponse(raw []byte) (archive.MaintenancePlan, error) {
	p, err := archiveClosedValue[archive.MaintenancePlan](raw, []string{"id", "archive_id", "archive_plan_id", "kind", "requester", "policy_hash", "target_hash", "created_at", "retired_at", "retain_until", "parents"}, nil)
	if err != nil || !p.Valid() {
		return archive.MaintenancePlan{}, strictjson.ErrObject
	}
	return p, nil
}
func decodeArchiveStatusResponse(raw []byte) (archive.MaintenanceStatus, error) {
	s, err := archiveClosedValue[archive.MaintenanceStatus](raw, []string{"archive_id", "state", "observed_at", "retired_at", "retain_until"}, []string{"reason", "last_verification", "deletion", "credentials"})
	if err != nil || !data.ValidID(string(s.ArchiveID)) || s.ObservedAt.IsZero() || s.RetiredAt.IsZero() || !s.RetainUntil.Equal(s.RetiredAt.Add(30*24*time.Hour)) {
		return archive.MaintenanceStatus{}, strictjson.ErrObject
	}
	switch s.State {
	case "unverified", "verified", "degraded", "expired", "deletion_pending", "local_deleted":
	default:
		return archive.MaintenanceStatus{}, strictjson.ErrObject
	}
	if s.Proof != nil && s.Proof.ArchiveID != s.ArchiveID || s.Deletion != nil && s.Deletion.ArchiveID != s.ArchiveID {
		return archive.MaintenanceStatus{}, strictjson.ErrObject
	}
	return s, nil
}
func decodeArchiveCredentialPlanResponse(raw []byte) (backupcredentials.ArchivePlan, error) {
	p, err := archiveClosedValue[backupcredentials.ArchivePlan](raw, []string{"plan_id", "kind", "requester", "scope", "version"}, []string{"expires_at"})
	if err != nil || !p.Valid() {
		return backupcredentials.ArchivePlan{}, strictjson.ErrObject
	}
	return p, nil
}
func decodeArchiveCredentialReceiptResponse(raw []byte) (backupcredentials.ArchiveReceipt, error) {
	r, err := archiveClosedValue[backupcredentials.ArchiveReceipt](raw, []string{"plan", "file", "received_at"}, nil)
	if err != nil || !r.Valid() {
		return backupcredentials.ArchiveReceipt{}, strictjson.ErrObject
	}
	return r, nil
}
