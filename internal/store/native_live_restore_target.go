package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"time"

	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/liverestore"
	"github.com/ShaulLavo/brine/internal/restic"
)

// NativeRestoreTargetObservation comes from held host descriptors and immutable
// material readback. It is neither an RPC payload nor independent authority.
type NativeRestoreTargetObservation struct {
	Parent         *os.Root
	Download       *os.Root
	OldTree        data.EngineTreeSeal
	TargetTree     data.EngineTreeSeal
	Allocation     data.AllocationReceipt
	UnitSHA256     string
	MaterialSHA256 string
	ObservedAt     time.Time
}

func (NativeRestoreTargetObservation) MarshalJSON() ([]byte, error) { return nil, ErrInvalid }

type nativeLiveRestoreTargetRecord struct {
	Format           string              `json:"format"`
	ID               string              `json:"id"`
	Attempt          liverestore.Attempt `json:"attempt"`
	VerifiedSHA256   string              `json:"verified_sha256"`
	IntentSHA256     string              `json:"intent_sha256"`
	RuntimeSHA256    string              `json:"runtime_sha256"`
	AllocationSHA256 string              `json:"allocation_sha256"`
	CheckpointSHA256 string              `json:"checkpoint_sha256"`
	MaterialSHA256   string              `json:"material_sha256"`
	OldTree          data.EngineTreeSeal `json:"old_tree"`
	TargetTree       data.EngineTreeSeal `json:"target_tree"`
	ObservedAt       time.Time           `json:"observed_at"`
}

func nativeRestoreMaterialSHA256(runtime data.EngineRuntime) string {
	raw, _ := json.Marshal(runtime.Secrets)
	return nativePointHash(raw)
}

func (s *Store) RecordNativeRestoreTarget(ctx context.Context, capability *NativeLiveRestoreExecution, extracted restic.AuthenticatedRestore, observed NativeRestoreTargetObservation) error {
	if s.readOnly || capability == nil || capability.state != s || !capability.stopped.active(s) {
		return ErrConflict
	}
	if _, err := s.ReadNativeStoppedPermit(ctx, capability.stopped); err != nil {
		return err
	}
	tx, cancel, err := s.beginWrite(ctx)
	defer cancel()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	retained, err := readNativeLiveRestoreAttempt(ctx, tx, capability.Attempt().OperationID)
	if err != nil || retained.claim.Attempt() != capability.Attempt() || retained.current.Stage() != liverestore.TransitionIntent {
		return errors.Join(ErrConflict, err)
	}
	if err = checkNativeLiveRestoreSourceTx(ctx, tx, capability.stopped, retained); err != nil {
		return err
	}
	verified, verifiedHash, err := readNativeLiveRestoreVerifiedTx(ctx, tx, retained)
	if err != nil {
		return err
	}
	facts, err := readNativeRestoreTargetFactsTx(ctx, tx, retained)
	if err != nil {
		return err
	}
	bundle, err := readNativeVerifiedPointBundleTx(ctx, tx, retained.claim.Attempt().SelectedPointID)
	if err != nil {
		return err
	}
	permit, err := readEnginePermit(ctx, tx, retained.claim.Attempt().BindingID)
	if err != nil {
		return err
	}
	if err = checkNativeRestoreTargetObservation(permit, facts, verified, observed); err != nil {
		return err
	}
	if !extracted.Matches(ctx, observed.Parent, "download", bundle.transport, bundle.manifest) {
		return ErrConflict
	}
	if err = checkNativeRestoreTargetAnchors(ctx, capability, verified, observed); err != nil {
		return err
	}
	id, err := data.NewID()
	if err != nil {
		return err
	}
	runtimeRaw, _ := json.Marshal(facts.Runtime)
	allocationRaw, _ := json.Marshal(observed.Allocation)
	record := nativeLiveRestoreTargetRecord{Format: "brine-native-live-restore-target-v1", ID: id, Attempt: retained.claim.Attempt(), VerifiedSHA256: verifiedHash, IntentSHA256: retained.current.SHA256(), RuntimeSHA256: nativePointHash(runtimeRaw), AllocationSHA256: nativePointHash(allocationRaw), CheckpointSHA256: facts.Checkpoint.SHA256(), MaterialSHA256: observed.MaterialSHA256, OldTree: observed.OldTree, TargetTree: observed.TargetTree, ObservedAt: observed.ObservedAt}
	raw, err := json.Marshal(record)
	if err != nil || len(raw) > 65536 || len(allocationRaw) > 16384 {
		return ErrInvalid
	}
	_, err = tx.ExecContext(ctx, "INSERT INTO data_native_live_restore_targets VALUES(?,?,?,?,?,?)", id, record.Attempt.OperationID, runtimeRaw, allocationRaw, facts.Checkpoint.Bytes(), raw)
	if err != nil {
		return err
	}
	if !capability.stopped.active(s) || !extracted.Matches(ctx, observed.Parent, "download", bundle.transport, bundle.manifest) || checkNativeRestoreTargetAnchors(ctx, capability, verified, observed) != nil {
		return ErrConflict
	}
	return tx.Commit()
}

func checkNativeRestoreTargetObservation(permit EnginePermit, facts NativeRestoreTargetFacts, verified nativeLiveRestoreVerifiedRecord, observed NativeRestoreTargetObservation) error {
	if observed.OldTree.Validate() != nil || observed.TargetTree.Validate() != nil || observed.OldTree.Root != verified.TargetTree.Root || observed.TargetTree.Root != verified.TargetTree.Root || observed.TargetTree.Parent != verified.TargetTree.Parent || observed.TargetTree.Directory == verified.TargetTree.Directory || observed.OldTree.Directory == observed.TargetTree.Directory || permit.Allocation == nil {
		return ErrConflict
	}
	a := permit.Allocation
	if observed.OldTree.Root != (data.EngineDirectoryIdentity{Device: a.RootDevice, Inode: a.RootInode}) || observed.OldTree.Directory != (data.EngineDirectoryIdentity{Device: a.DirectoryDevice, Inode: a.DirectoryInode}) || !nativeAllocationMatches(observed.Allocation, permit.Reserved, observed.TargetTree) || observed.Allocation.MappingProof.Runtime != facts.Runtime.Runtime {
		return ErrConflict
	}
	if observed.UnitSHA256 != facts.Runtime.UnitSHA256 || observed.MaterialSHA256 != nativeRestoreMaterialSHA256(facts.Runtime) || observed.ObservedAt.IsZero() || observed.ObservedAt.Location() != time.UTC || observed.ObservedAt.Before(verified.VerifiedAt) || observed.ObservedAt.After(time.Now().UTC()) {
		return ErrConflict
	}
	return nil
}

// CheckNativeLiveRestoreRetainedSource keeps the same held source/consumer after
// verification. It does not reserve a second extraction or extend admission.
func (s *Store) CheckNativeLiveRestoreRetainedSource(ctx context.Context, capability *NativeLiveRestoreExecution) error {
	if capability == nil || capability.state != s || !capability.stopped.active(s) {
		return ErrConflict
	}
	if _, err := s.ReadNativeStoppedPermit(ctx, capability.stopped); err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	retained, err := readNativeLiveRestoreAttempt(ctx, tx, capability.Attempt().OperationID)
	if err != nil || retained.claim.Attempt() != capability.Attempt() {
		return errors.Join(ErrConflict, err)
	}
	switch retained.current.Stage() {
	case liverestore.RestoreIntent, liverestore.Verified, liverestore.TransitionIntent:
	default:
		return ErrConflict
	}
	if err = checkNativeLiveRestoreSourceTx(ctx, tx, capability.stopped, retained); err != nil {
		return err
	}
	return tx.Commit()
}
