package data

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
)

type EngineTransitionCommon struct {
	Engine                EngineKind              `json:"engine"`
	Scope                 EngineTransitionScope   `json:"scope"`
	Base                  EngineTransitionBase    `json:"base"`
	NewEpochID            ReplicaEpochID          `json:"new_epoch_id"`
	FenceID               FenceID                 `json:"fence_id"`
	FenceOwnerOperationID string                  `json:"fence_owner_operation_id"`
	PreparedPoint         EngineEvidenceRef       `json:"prepared_point"`
	SourceWitness         EngineEvidenceRef       `json:"source_witness"`
	OldTree               EngineTreeSeal          `json:"old_tree"`
	TargetTree            EngineTreeSeal          `json:"target_tree"`
	PreservedParent       EngineDirectoryIdentity `json:"preserved_parent"`
	TargetVerification    EngineEvidenceRef       `json:"target_verification"`
	TargetRuntimeSHA256   string                  `json:"target_runtime_sha256"`
	TargetCheckpoint      EngineEvidenceRef       `json:"target_checkpoint"`
	TargetMaterial        EngineEvidenceRef       `json:"target_material"`
	RecoveryRelease       EngineEvidenceRef       `json:"recovery_release"`
	RetainedDependencies  EngineEvidenceRef       `json:"retained_dependencies"`
}

func (c EngineTransitionCommon) validate() error {
	if c.Engine != EngineSpacetimeDB || c.Scope.Validate() != nil || !c.Base.valid() || c.Base.binding() != c.Scope.BindingID ||
		!ValidID(string(c.NewEpochID)) || c.NewEpochID == c.Base.epoch() || !ValidID(string(c.FenceID)) ||
		!engineOperationID(c.FenceOwnerOperationID) || !engineDigest(c.TargetRuntimeSHA256) {
		return ErrInvalid
	}
	if c.OldTree.Validate() != nil || c.TargetTree.Validate() != nil || c.OldTree.Root != c.TargetTree.Root ||
		c.OldTree.Directory == c.TargetTree.Directory || !c.PreservedParent.valid() || c.PreservedParent.Device != c.OldTree.Root.Device ||
		c.PreservedParent == c.OldTree.Directory || c.PreservedParent == c.TargetTree.Directory {
		return ErrInvalid
	}
	if base, ok := c.Base.Unpublished(); ok && base.Allocation.DatabaseID != c.Scope.DatabaseID {
		return ErrInvalid
	}
	return c.validateEvidence()
}

func (c EngineTransitionCommon) validateEvidence() error {
	for _, row := range []struct {
		ref   EngineEvidenceRef
		epoch ReplicaEpochID
		kind  EngineEvidenceKind
	}{
		{c.PreparedPoint, c.Base.epoch(), EnginePreparedPointEvidence},
		{c.SourceWitness, c.Base.epoch(), EngineSourceWitnessEvidence},
		{c.TargetVerification, c.NewEpochID, EngineTargetEvidence},
		{c.TargetCheckpoint, c.NewEpochID, EngineCheckpointEvidence},
		{c.TargetMaterial, c.NewEpochID, EngineMaterialEvidence},
		{c.RecoveryRelease, c.NewEpochID, EngineRecoveryEvidence},
		{c.RetainedDependencies, c.NewEpochID, EngineDependenciesEvidence},
	} {
		if !row.ref.matches(c.Scope, row.epoch, row.kind) {
			return ErrInvalid
		}
	}
	return nil
}

type EnginePublicationTarget struct {
	CandidateSHA256 string            `json:"candidate_sha256"`
	Receipt         EngineEvidenceRef `json:"receipt"`
}

type EngineRestoreTarget struct {
	SelectedPoint EngineEvidenceRef `json:"selected_point"`
}

// EngineTransition is an immutable journal input, not a source or launch proof.
type EngineTransition struct {
	common      EngineTransitionCommon
	purpose     string
	publication EnginePublicationTarget
	restoration EngineRestoreTarget
	encoded     []byte
	hash        string
}

func NewEnginePublicationTransition(common EngineTransitionCommon, target EnginePublicationTarget) (EngineTransition, error) {
	if common.validate() != nil || !engineDigest(target.CandidateSHA256) || !target.Receipt.matches(common.Scope, common.NewEpochID, EnginePublicationEvidence) {
		return EngineTransition{}, ErrInvalid
	}
	return encodeEngineTransition(EngineTransition{common: common, purpose: "publish", publication: target})
}

func NewEngineRestoreTransition(common EngineTransitionCommon, target EngineRestoreTarget) (EngineTransition, error) {
	if common.validate() != nil || common.Base.Kind() != "committed" || !target.SelectedPoint.validFor(EngineRestorePointEvidence) ||
		target.SelectedPoint.DatabaseID != common.Scope.DatabaseID || target.SelectedPoint.BindingID != common.Scope.BindingID || target.SelectedPoint.EpochID == common.NewEpochID {
		return EngineTransition{}, ErrInvalid
	}
	return encodeEngineTransition(EngineTransition{common: common, purpose: "restore", restoration: target})
}

func (t EngineTransition) Common() EngineTransitionCommon { return t.common }
func (t EngineTransition) Purpose() string                { return t.purpose }
func (t EngineTransition) SHA256() string                 { return t.hash }
func (t EngineTransition) Bytes() []byte                  { return bytes.Clone(t.encoded) }
func (t EngineTransition) Publication() (EnginePublicationTarget, bool) {
	return t.publication, t.purpose == "publish"
}
func (t EngineTransition) Restoration() (EngineRestoreTarget, bool) {
	return t.restoration, t.purpose == "restore"
}

func (t EngineTransition) MarshalJSON() ([]byte, error) {
	if t.hash == "" || len(t.encoded) == 0 {
		return nil, ErrInvalid
	}
	return t.Bytes(), nil
}

func encodeEngineTransition(t EngineTransition) (EngineTransition, error) {
	var target any = t.publication
	if t.purpose == "restore" {
		target = t.restoration
	}
	raw, err := json.Marshal(map[string]any{"format": EngineTransitionFormat, "purpose": t.purpose, "common": t.common, t.purpose: target})
	if err != nil || len(raw) > MaxEngineTransitionBytes {
		return EngineTransition{}, ErrInvalid
	}
	sum := sha256.Sum256(raw)
	t.encoded, t.hash = raw, hex.EncodeToString(sum[:])
	return t, nil
}
