package store

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"time"

	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/datapublish"
	"github.com/ShaulLavo/brine/internal/nativecapture"
)

// PublicationTargetObservation contains actual held host descriptors and
// readback. It cannot be decoded from an operator request.
type PublicationTargetObservation struct {
	Parent         *os.Root
	Staged         *os.Root
	OldTree        data.EngineTreeSeal
	TargetTree     data.EngineTreeSeal
	Allocation     data.AllocationReceipt
	UnitSHA256     string
	UnitAbsent     bool
	MaterialSHA256 string
	ObservedAt     time.Time
}

func (PublicationTargetObservation) MarshalJSON() ([]byte, error) { return nil, ErrInvalid }

type publicationTargetRecord struct {
	Format           string                 `json:"format"`
	Scope            datapublish.ProofScope `json:"scope"`
	TransitionID     string                 `json:"transition_id"`
	IntentSHA256     string                 `json:"intent_sha256"`
	RuntimeSHA256    string                 `json:"runtime_sha256"`
	CheckpointSHA256 string                 `json:"checkpoint_sha256"`
	MaterialSHA256   string                 `json:"material_sha256"`
	UnitAbsent       bool                   `json:"unit_absent,omitempty"`
	OldTree          data.EngineTreeSeal    `json:"old_tree"`
	TargetTree       data.EngineTreeSeal    `json:"target_tree"`
	Allocation       data.AllocationReceipt `json:"allocation"`
	ObservedAt       time.Time              `json:"observed_at"`
}

type publicationTarget struct {
	record     publicationTargetRecord
	hash       string
	runtime    data.EngineRuntime
	checkpoint nativecapture.Checkpoint
}

func (s *Store) RecordPublicationTarget(ctx context.Context, capability *PublicationTargetPreparation, observed PublicationTargetObservation) error {
	if !capability.active(s) || capability.attempt.restored.unchanged(ctx) != nil || !capability.attempt.restored.working {
		return ErrConflict
	}
	tx, cancel, err := s.beginWrite(ctx)
	defer cancel()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	current, intent, intentHash, checkpoint, err := s.readPublicationTargetPreparationTx(ctx, tx, capability)
	if err != nil {
		return err
	}
	permit, err := readEnginePermit(ctx, tx, capability.attempt.binding)
	if err != nil || checkPublicationTargetObservation(permit, intent, observed) != nil || checkPublicationTargetAnchors(ctx, capability, observed) != nil || checkPublicationCheckpointMembers(ctx, observed.Staged, checkpoint) != nil {
		return errors.Join(ErrConflict, err)
	}
	runtimeRaw, _ := json.Marshal(intent.Runtime)
	record := publicationTargetRecord{Format: "brine-publication-target-v1", Scope: current.cursor.Scope(), TransitionID: intent.TransitionID, IntentSHA256: intentHash, RuntimeSHA256: publicationStorageSHA256(runtimeRaw), CheckpointSHA256: checkpoint.SHA256(), MaterialSHA256: observed.MaterialSHA256, UnitAbsent: observed.UnitAbsent, OldTree: observed.OldTree, TargetTree: observed.TargetTree, Allocation: cloneNativeAllocation(observed.Allocation), ObservedAt: observed.ObservedAt}
	raw, err := json.Marshal(record)
	if err != nil || len(raw) > 65536 {
		return ErrInvalid
	}
	if err = insertPublicationProofTx(ctx, tx, capability.attempt.planID, "target", publicationTransitionTargetProof, raw); err != nil {
		return err
	}
	if !capability.active(s) || capability.attempt.restored.unchanged(ctx) != nil || checkPublicationTargetAnchors(ctx, capability, observed) != nil || checkPublicationCheckpointMembers(ctx, observed.Staged, checkpoint) != nil {
		return ErrConflict
	}
	return tx.Commit()
}

func (s *Store) readPublicationTargetPreparationTx(ctx context.Context, tx *sql.Tx, capability *PublicationTargetPreparation) (publicationAttemptState, publicationTargetIntent, string, nativecapture.Checkpoint, error) {
	current, err := s.readPublicationAttemptTx(ctx, tx, capability.attempt)
	intent, hash, intentErr := readPublicationTargetIntentTx(ctx, tx, capability.attempt.planID)
	step, ready := current.cursor.NextStep()
	if err != nil || intentErr != nil || !ready || step != datapublish.FinalTransition || hash != capability.intentSHA || intent.Scope != current.cursor.Scope() || intent.TransitionID != current.claim.RepositoryIntentID {
		return current, intent, hash, nativecapture.Checkpoint{}, errors.Join(ErrConflict, err, intentErr)
	}
	if _, _, err = readPublicationDisposalTx(ctx, tx, capability.attempt, current); err != nil {
		return current, intent, hash, nativecapture.Checkpoint{}, err
	}
	checkpoint, err := nativecapture.DecodeCheckpoint(intent.Checkpoint, nativecapture.DefaultLimits())
	return current, intent, hash, checkpoint, err
}

func checkPublicationTargetObservation(permit EnginePermit, intent publicationTargetIntent, observed PublicationTargetObservation) error {
	a := permit.Allocation
	if a == nil || observed.OldTree.Validate() != nil || observed.TargetTree.Validate() != nil || observed.OldTree.Root != observed.TargetTree.Root || observed.OldTree.Directory == observed.TargetTree.Directory || observed.TargetTree.Parent == observed.OldTree.Directory {
		return ErrConflict
	}
	if observed.OldTree.Root != (data.EngineDirectoryIdentity{Device: a.RootDevice, Inode: a.RootInode}) || observed.OldTree.Directory != (data.EngineDirectoryIdentity{Device: a.DirectoryDevice, Inode: a.DirectoryInode}) || !nativeAllocationMatches(observed.Allocation, permit.Reserved, observed.TargetTree) || observed.Allocation.MappingProof.Runtime != intent.Runtime.Runtime {
		return ErrConflict
	}
	if !publicationTargetUnitMatches(permit.Runtime.State == data.EngineUnpublished, intent.Runtime.UnitSHA256, observed.UnitSHA256, observed.UnitAbsent) || observed.MaterialSHA256 != nativeRestoreMaterialSHA256(intent.Runtime) || observed.ObservedAt.Location() != time.UTC || observed.ObservedAt.Before(intent.RecordedAt) || observed.ObservedAt.After(time.Now().UTC()) {
		return ErrConflict
	}
	return nil
}

func publicationTargetUnitMatches(initial bool, expected, observed string, absent bool) bool {
	if initial {
		return absent && observed == ""
	}
	return !absent && data.ValidCatalogHash(expected) && observed == expected
}

func publicationTargetUnitSHA256(target publicationTarget) string {
	if target.record.UnitAbsent {
		return ""
	}
	return target.runtime.UnitSHA256
}

func readPublicationTargetTx(ctx context.Context, tx *sql.Tx, current publicationAttemptState) (publicationTarget, error) {
	var target publicationTarget
	raw, hash, err := readPublicationProofTx(ctx, tx, current.review.Plan().ID(), "target", "transition_target")
	if err != nil || len(raw) > 65536 || json.Unmarshal(raw, &target.record) != nil {
		return target, errors.Join(&IntegrityError{}, err)
	}
	r := target.record
	canonical, _ := json.Marshal(r)
	intent, intentHash, err := readPublicationTargetIntentTx(ctx, tx, current.review.Plan().ID())
	runtimeRaw, _ := json.Marshal(intent.Runtime)
	if err != nil || !bytes.Equal(raw, canonical) || r.Format != "brine-publication-target-v1" || r.Scope != current.cursor.Scope() || r.TransitionID != current.claim.RepositoryIntentID || r.IntentSHA256 != intentHash || r.RuntimeSHA256 != publicationStorageSHA256(runtimeRaw) {
		return target, errors.Join(&IntegrityError{}, err)
	}
	target.checkpoint, err = nativecapture.DecodeCheckpoint(intent.Checkpoint, nativecapture.DefaultLimits())
	permit, permitErr := readPublicationSourcePermitTx(ctx, tx, current)
	target.runtime = intent.Runtime.Clone()
	observed := PublicationTargetObservation{OldTree: r.OldTree, TargetTree: r.TargetTree, Allocation: r.Allocation, UnitSHA256: publicationTargetUnitSHA256(target), UnitAbsent: r.UnitAbsent, MaterialSHA256: r.MaterialSHA256, ObservedAt: r.ObservedAt}
	if err != nil || permitErr != nil || target.checkpoint.SHA256() != r.CheckpointSHA256 || checkPublicationTargetObservation(permit, intent, observed) != nil {
		return target, errors.Join(&IntegrityError{}, err, permitErr)
	}
	target.hash = hash
	return target, nil
}

func readPublicationSourcePermitTx(ctx context.Context, tx *sql.Tx, current publicationAttemptState) (EnginePermit, error) {
	reserved, err := readReservedEngine(ctx, tx, current.claim.BindingID)
	if err != nil {
		return EnginePermit{}, err
	}
	var raw []byte
	if err = tx.QueryRowContext(ctx, "SELECT canonical FROM data_engine_runtime_commits WHERE binding_id=? AND version=?", current.claim.BindingID, current.index.runtimeVersion).Scan(&raw); err != nil {
		return EnginePermit{}, err
	}
	runtime, err := decodeNativeRestoreRuntime(raw)
	if err != nil || publicationStorageSHA256(raw) != current.index.runtimeSHA || runtime.EpochID != current.claim.OldEpochID {
		return EnginePermit{}, errors.Join(&IntegrityError{}, err)
	}
	head := data.EngineHeadRef{BindingID: current.claim.BindingID, EpochID: current.claim.OldEpochID, Version: current.index.runtimeVersion, RuntimeSHA256: current.index.runtimeSHA}
	reserved, allocation, err := projectEngineEpoch(ctx, tx, reserved, head)
	if err != nil {
		return EnginePermit{}, err
	}
	if allocation == nil {
		initial, readErr := readInitialEngineAllocation(ctx, tx, reserved)
		if readErr != nil {
			return EnginePermit{}, readErr
		}
		allocation = &initial
	}
	return EnginePermit{Reserved: reserved, Runtime: runtime, Allocation: allocation}, nil
}
