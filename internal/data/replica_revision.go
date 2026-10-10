package data

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
)

// RevisionStored records a pending immutable config before any host effect.
const RevisionStored RotationStage = "stored"

// ReplicaRevision journals only cadence/config activation, never writer admission.
type ReplicaRevision struct {
	ID     string         `json:"id"`
	App    string         `json:"app"`
	Before ReplicaBinding `json:"before"`
	After  ReplicaBinding `json:"after"`
	Stage  RotationStage  `json:"stage"`
}

func (r ReplicaRevision) Validate() error {
	if !strings.HasPrefix(r.ID, "sha256:") || !ValidCatalogHash(strings.TrimPrefix(r.ID, "sha256:")) || !namePattern.MatchString(r.App) || !r.Before.Committed || !r.After.Committed || !ValidID(string(r.Before.BindingID)) || !ValidID(string(r.Before.DatabaseID)) || !ValidID(string(r.Before.EpochID)) || !ValidCatalogHash(r.Before.ConfigSHA256) || !ValidCatalogHash(r.After.ConfigSHA256) || !ValidCatalogHash(r.After.UnitSHA256) || r.Before.ConfigSHA256 == r.After.ConfigSHA256 || r.Before.ConfigFile == r.After.ConfigFile {
		return ErrInvalid
	}
	for _, binding := range []ReplicaBinding{r.Before, r.After} {
		sum := sha256.Sum256([]byte(binding.ConfigContent))
		if len(binding.ConfigContent) == 0 || len(binding.ConfigContent) > 65536 || hex.EncodeToString(sum[:]) != binding.ConfigSHA256 || !ValidCatalogHash(binding.UnitSHA256) || !ValidRoot(binding.ConfigFile) {
			return ErrInvalid
		}
	}
	before := r.Before
	before.ConfigContent = r.After.ConfigContent
	before.ConfigSHA256 = r.After.ConfigSHA256
	before.ConfigFile = r.After.ConfigFile
	before.UnitSHA256 = r.After.UnitSHA256
	if before != r.After {
		return ErrInvalid
	}
	switch r.Stage {
	case RevisionStored, RotationPrepared, RotationStopIssued, RotationStopped, RotationCommitted, RotationStartIssued, RotationActive:
		return nil
	default:
		return ErrInvalid
	}
}

func (r ReplicaRevision) Follows(previous RotationStage) bool {
	if r.Stage == RevisionStored {
		return previous == ""
	}
	if r.Stage == RotationPrepared {
		return previous == RevisionStored
	}
	return (CredentialRotation{Stage: r.Stage}).Follows(previous)
}
