package data

import (
	"encoding/hex"
	"math"
	"strings"
	"time"
)

const EngineTransitionFormat = "brine-engine-transition-v1"
const MaxEngineTransitionBytes = 64 << 10

type EngineTransitionScope struct {
	ID               string           `json:"id"`
	ApplyOperationID string           `json:"apply_operation_id"`
	PlanID           string           `json:"plan_id"`
	Requester        string           `json:"requester"`
	PolicyHash       string           `json:"policy_hash"`
	TargetHash       string           `json:"target_hash"`
	DesiredHash      string           `json:"desired_hash"`
	Generation       uint64           `json:"generation"`
	IncarnationID    AppIncarnationID `json:"incarnation_id"`
	DatabaseID       DatabaseID       `json:"database_id"`
	BindingID        ReplicaBindingID `json:"binding_id"`
	ExpiresAt        time.Time        `json:"expires_at"`
}

func (s EngineTransitionScope) Validate() error {
	if !ValidID(s.ID) || !engineOperationID(s.ApplyOperationID) || !enginePlanHash(s.PlanID) || !engineRequester(s.Requester) ||
		!enginePlanHash(s.PolicyHash) || !enginePlanHash(s.TargetHash) || !enginePlanHash(s.DesiredHash) ||
		!ValidID(string(s.IncarnationID)) || !ValidID(string(s.DatabaseID)) || !ValidID(string(s.BindingID)) || s.ExpiresAt.IsZero() {
		return ErrInvalid
	}
	return nil
}

type EngineHeadRef struct {
	BindingID     ReplicaBindingID `json:"binding_id"`
	EpochID       ReplicaEpochID   `json:"epoch_id"`
	Version       uint64           `json:"version"`
	RuntimeSHA256 string           `json:"runtime_sha256"`
}

func (h EngineHeadRef) Validate() error {
	if !ValidID(string(h.BindingID)) || !ValidID(string(h.EpochID)) || h.Version == 0 || h.Version > math.MaxInt64 || !engineDigest(h.RuntimeSHA256) {
		return ErrInvalid
	}
	return nil
}

type EngineDirectoryIdentity struct {
	Device uint64 `json:"device"`
	Inode  uint64 `json:"inode"`
}

func (d EngineDirectoryIdentity) valid() bool { return d.Device != 0 && d.Inode != 0 }

type EngineTreeSeal struct {
	Root           EngineDirectoryIdentity `json:"root"`
	Parent         EngineDirectoryIdentity `json:"parent"`
	Directory      EngineDirectoryIdentity `json:"directory"`
	ContentsSHA256 string                  `json:"contents_sha256"`
}

func (s EngineTreeSeal) Validate() error {
	if !s.Root.valid() || !s.Parent.valid() || !s.Directory.valid() || s.Parent.Device != s.Root.Device ||
		s.Directory.Device != s.Root.Device || s.Directory == s.Root || s.Directory == s.Parent || !engineDigest(s.ContentsSHA256) {
		return ErrInvalid
	}
	return nil
}

type EngineEvidenceKind string

const (
	EngineAllocationEvidence    EngineEvidenceKind = "allocation"
	EnginePreparedPointEvidence EngineEvidenceKind = "prepared_point"
	EngineSourceWitnessEvidence EngineEvidenceKind = "source_witness"
	EngineTargetEvidence        EngineEvidenceKind = "target_verification"
	EngineCheckpointEvidence    EngineEvidenceKind = "target_checkpoint"
	EngineMaterialEvidence      EngineEvidenceKind = "target_material"
	EngineRecoveryEvidence      EngineEvidenceKind = "recovery_release"
	EngineDependenciesEvidence  EngineEvidenceKind = "retained_dependencies"
	EnginePublicationEvidence   EngineEvidenceKind = "publication_receipt"
	EngineRestorePointEvidence  EngineEvidenceKind = "restore_point"
	EngineCompletionEvidence    EngineEvidenceKind = "completion"
	EngineAbandonEvidence       EngineEvidenceKind = "abandoned_current"
)

// EngineEvidenceRef locates evidence; valid syntax does not establish its authority.
type EngineEvidenceRef struct {
	Kind       EngineEvidenceKind `json:"kind"`
	ID         string             `json:"id"`
	SHA256     string             `json:"sha256"`
	DatabaseID DatabaseID         `json:"database_id"`
	BindingID  ReplicaBindingID   `json:"binding_id"`
	EpochID    ReplicaEpochID     `json:"epoch_id"`
}

func (r EngineEvidenceRef) validFor(kind EngineEvidenceKind) bool {
	return r.Kind == kind && (ValidID(r.ID) || enginePlanHash(r.ID)) && engineDigest(r.SHA256) &&
		ValidID(string(r.DatabaseID)) && ValidID(string(r.BindingID)) && ValidID(string(r.EpochID))
}

func (r EngineEvidenceRef) matches(s EngineTransitionScope, epoch ReplicaEpochID, kind EngineEvidenceKind) bool {
	return r.validFor(kind) && r.DatabaseID == s.DatabaseID && r.BindingID == s.BindingID && r.EpochID == epoch
}

func engineDigest(value string) bool {
	if len(value) != 64 || value != strings.ToLower(value) {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func enginePlanHash(value string) bool {
	return strings.HasPrefix(value, "sha256:") && engineDigest(strings.TrimPrefix(value, "sha256:"))
}

func engineRequester(value string) bool {
	return strings.HasPrefix(value, "deploy:") && engineDigest(strings.TrimPrefix(value, "deploy:"))
}

func engineOperationID(value string) bool {
	if len(value) == 0 || len(value) > 128 || value[0] == '_' || value[0] == '-' {
		return false
	}
	for _, c := range value {
		if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_' || c == '-' {
			continue
		}
		return false
	}
	return true
}
