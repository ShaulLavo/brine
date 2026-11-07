package archive

import (
	"encoding/json"
	"time"

	"github.com/ShaulLavo/brine/internal/data"
)

type RemovalReceipt struct {
	Format             string         `json:"format"`
	OperationID        string         `json:"operation_id"`
	AttemptOperationID string         `json:"attempt_operation_id"`
	PlanID             string         `json:"plan_id"`
	ArchiveID          data.ArchiveID `json:"archive_id"`
	ManifestSHA256     string         `json:"manifest_sha256"`
	CompletionSHA256   string         `json:"completion_sha256"`
	Outcome            string         `json:"outcome"`
	RetainUntil        time.Time      `json:"retain_until"`
}

func (r RemovalReceipt) Valid() bool {
	if r.Format != "brine-sqlite-archive-receipt-v1" || !nativeArchiveOperationID.MatchString(r.OperationID) || !nativeArchiveOperationID.MatchString(r.AttemptOperationID) || !digest.MatchString(r.PlanID) || !data.ValidID(string(r.ArchiveID)) || !data.ValidCatalogHash(r.CompletionSHA256) || r.ManifestSHA256 != "" && !data.ValidCatalogHash(r.ManifestSHA256) {
		return false
	}
	if r.Outcome == "retired" {
		return r.ManifestSHA256 != "" && removalTime(r.RetainUntil)
	}
	return r.Outcome == "abandoned" && r.RetainUntil.IsZero()
}

func EncodeRemovalReceipt(r RemovalReceipt) ([]byte, error) {
	if !r.Valid() {
		return nil, ErrRefused
	}
	raw, err := json.Marshal(r)
	if err != nil || len(raw) > 4096 {
		return nil, ErrRefused
	}
	return raw, nil
}

func DecodeRemovalReceipt(raw []byte) (RemovalReceipt, error) {
	if len(raw) == 0 || len(raw) > 4096 {
		return RemovalReceipt{}, ErrRefused
	}
	_, err := removalObject(raw, "format", "operation_id", "attempt_operation_id", "plan_id", "archive_id", "manifest_sha256", "completion_sha256", "outcome", "retain_until")
	if err != nil {
		return RemovalReceipt{}, ErrRefused
	}
	var receipt RemovalReceipt
	if json.Unmarshal(raw, &receipt) != nil || !receipt.Valid() {
		return RemovalReceipt{}, ErrRefused
	}
	return receipt, nil
}
