package data

import (
	"strings"
	"time"
)

type RotationStage string

const (
	RotationPrepared    RotationStage = "prepared"
	RotationStopIssued  RotationStage = "stop_issued"
	RotationStopped     RotationStage = "stopped"
	RotationCommitted   RotationStage = "committed"
	RotationStartIssued RotationStage = "start_issued"
	RotationActive      RotationStage = "active"
	RotationVerified    RotationStage = "verified"
	RotationSuperseded  RotationStage = "superseded"
)

// CredentialRotation contains only immutable binding references and a durable
// effect cursor. It is never credential material or permission to retry effects.
type CredentialRotation struct {
	PlanID       string         `json:"plan_id"`
	App          string         `json:"app"`
	Before       ReplicaBinding `json:"before"`
	After        ReplicaBinding `json:"after"`
	Stage        RotationStage  `json:"stage"`
	ExpiresAt    string         `json:"expires_at,omitempty"`
	SupersededBy string         `json:"superseded_by,omitempty"`
}

func (r CredentialRotation) Validate() error {
	if !strings.HasPrefix(r.PlanID, "sha256:") || !ValidCatalogHash(strings.TrimPrefix(r.PlanID, "sha256:")) || !namePattern.MatchString(r.App) || !r.Before.Committed || !r.After.Committed || !ValidID(string(r.Before.DatabaseID)) || !ValidID(string(r.Before.BindingID)) || !ValidID(string(r.Before.EpochID)) || r.Before.CredentialVersion == 0 || !ValidRoot(r.Before.CredentialFile) || !ValidRoot(r.After.CredentialFile) || r.After.CredentialVersion <= r.Before.CredentialVersion || !ValidCatalogHash(r.After.UnitSHA256) {
		return ErrInvalid
	}
	if r.ExpiresAt != "" {
		expiry, err := time.Parse(time.RFC3339Nano, r.ExpiresAt)
		if err != nil || !strings.HasSuffix(r.ExpiresAt, "Z") || expiry.IsZero() {
			return ErrInvalid
		}
	}
	if (r.Stage == RotationSuperseded) != (r.SupersededBy != "") || r.SupersededBy != "" && (!strings.HasPrefix(r.SupersededBy, "sha256:") || !ValidCatalogHash(strings.TrimPrefix(r.SupersededBy, "sha256:")) || r.SupersededBy == r.PlanID) {
		return ErrInvalid
	}
	old, new := r.Before, r.After
	old.CredentialVersion = new.CredentialVersion
	old.CredentialFile = new.CredentialFile
	old.UnitSHA256 = new.UnitSHA256
	if old != new {
		return ErrInvalid
	}
	switch r.Stage {
	case RotationPrepared, RotationStopIssued, RotationStopped, RotationCommitted, RotationStartIssued, RotationActive, RotationVerified, RotationSuperseded:
		return nil
	default:
		return ErrInvalid
	}
}
func (r CredentialRotation) Follows(previous RotationStage) bool {
	switch r.Stage {
	case RotationPrepared:
		return previous == ""
	case RotationStopIssued:
		return previous == RotationPrepared
	case RotationStopped:
		return previous == RotationStopIssued
	case RotationCommitted:
		return previous == RotationStopped
	case RotationStartIssued:
		return previous == RotationCommitted
	case RotationActive:
		return previous == RotationStartIssued
	case RotationVerified:
		return previous == RotationActive
	default:
		return false
	}
}

func (r CredentialRotation) Expired(now time.Time) bool {
	expiry, err := time.Parse(time.RFC3339Nano, r.ExpiresAt)
	return err == nil && !now.Before(expiry)
}
