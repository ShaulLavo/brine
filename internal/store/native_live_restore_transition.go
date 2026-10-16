package store

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"

	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/liverestore"
	"github.com/ShaulLavo/brine/internal/ops"
	"github.com/ShaulLavo/brine/internal/restic"
)

// NativeRestoreTransition holds the original attempt and actual host locks
// through relocation, activation and completion. It is never a decoded proof.
type NativeRestoreTransition struct {
	state              *Store
	binding            data.SpacetimeDBBinding
	preparation        *EnginePreparation
	lifetime           *data.EngineLock
	attempt            liverestore.Attempt
	inputSHA256        string
	resolveOperationID string
	inspectOriginal    bool
}

func (NativeRestoreTransition) MarshalJSON() ([]byte, error) { return nil, ErrInvalid }
func (NativeRestoreTransition) String() string               { return "[private native restore transition]" }
func (NativeRestoreTransition) GoString() string             { return "[private native restore transition]" }
func (p *NativeRestoreTransition) active(s *Store) bool {
	return p != nil && p.state == s && !s.readOnly && p.preparation != nil && p.preparation.StateRoot() == s.dir && p.preparation.Active(p.attempt.BindingID) && p.lifetime.HoldsLifetime(s.dir, p.attempt.BindingID)
}

type NativeRestoreTransitionView struct {
	Input      data.EngineTransition
	Cursor     data.EngineTransitionCursor
	Runtime    data.EngineRuntime
	Allocation data.AllocationReceipt
}

func (s *Store) ClaimNativeLiveRestoreTransition(ctx context.Context, capability *NativeLiveRestoreExecution, incoming *NativeIncomingRepository, initialized restic.InitializedRepository) (*NativeRestoreTransition, error) {
	if capability == nil || capability.state != s || !capability.stopped.active(s) || !incoming.active(s) {
		return nil, ErrConflict
	}
	if _, err := s.ReadNativeStoppedPermit(ctx, capability.stopped); err != nil {
		return nil, err
	}
	recorded, err := s.ReadNativeIncomingInitialized(ctx, incoming)
	if err != nil || recorded.Scope != initialized.Scope() || recorded.ConfigSHA256 != initialized.ConfigurationSHA256() || !recorded.VerifiedAt.Equal(initialized.VerifiedAt()) {
		return nil, ErrConflict
	}
	recordedRaw, err := json.Marshal(recorded)
	if err != nil {
		return nil, err
	}
	tx, cancel, err := s.beginWrite(ctx)
	defer cancel()
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	retained, err := readNativeLiveRestoreAttempt(ctx, tx, capability.Attempt().OperationID)
	if err != nil || retained.claim.Attempt() != capability.Attempt() || retained.current.Stage() != liverestore.TransitionIntent {
		return nil, errors.Join(ErrConflict, err)
	}
	if err = checkNativeLiveRestoreSourceTx(ctx, tx, capability.stopped, retained); err != nil {
		return nil, err
	}
	intent, err := s.readNativeIncomingRepositoryTx(ctx, tx, incoming)
	if err != nil || intent.record.OperationID != capability.Attempt().OperationID || intent.epochID != capability.Attempt().IncomingEpochID || intent.record.ConsumerSHA256 != capability.Attempt().ConsumerSHA256 {
		return nil, errors.Join(ErrConflict, err)
	}
	scope := initialized.Scope()
	scope.RepositoryID = ""
	if !nativeIncomingTransitionScopeMatches(scope, intent) || !initialized.AdmitsCreation(scope, intent.creationGrantSHA256) {
		return nil, ErrConflict
	}
	var recordedRepository string
	var retainedRaw []byte
	err = tx.QueryRowContext(ctx, "SELECT repository_id,canonical FROM data_native_repository_results WHERE intent_id=?", intent.creationID).Scan(&recordedRepository, &retainedRaw)
	if err != nil || recordedRepository != initialized.Scope().RepositoryID || !bytes.Equal(retainedRaw, recordedRaw) {
		return nil, errors.Join(ErrConflict, err)
	}
	target, err := readNativeRestoreTargetTx(ctx, tx, retained)
	if err != nil {
		return nil, err
	}
	input, err := nativeRestoreTransitionInput(retained, target)
	if err != nil {
		return nil, err
	}
	if _, err = s.insertEngineTransitionTx(ctx, tx, capability.stopped.preparation.preparation, input, recordedRepository); err != nil {
		return nil, err
	}
	transition := &NativeRestoreTransition{state: s, binding: capability.Binding(), preparation: capability.stopped.preparation.preparation, lifetime: capability.stopped.lifetime, attempt: retained.claim.Attempt(), inputSHA256: input.SHA256()}
	if !transition.active(s) {
		return nil, ErrConflict
	}
	return transition, tx.Commit()
}

func nativeRestoreTransitionInput(retained nativeLiveRestoreAttempt, target nativeRestoreTarget) (data.EngineTransition, error) {
	a, spec, r := retained.claim.Attempt(), retained.plan.Spec(), target.record
	scope := data.EngineTransitionScope{ID: a.TransitionID, ApplyOperationID: a.OperationID, PlanID: a.PlanID, Requester: spec.Requester.String(), PolicyHash: "sha256:" + spec.Facts.PolicySHA256, TargetHash: "sha256:" + spec.Facts.TargetSHA256, DesiredHash: "sha256:" + spec.Facts.DesiredSHA256, Generation: spec.Facts.Generation, IncarnationID: spec.Facts.IncarnationID, DatabaseID: spec.Facts.DatabaseID, BindingID: a.BindingID, ExpiresAt: spec.ExpiresAt}
	ref := func(kind data.EngineEvidenceKind, id, hash string, epoch data.ReplicaEpochID) data.EngineEvidenceRef {
		return data.EngineEvidenceRef{Kind: kind, ID: id, SHA256: hash, DatabaseID: scope.DatabaseID, BindingID: scope.BindingID, EpochID: epoch}
	}
	base, err := data.CommittedEngineBase(spec.Facts.Head)
	if err != nil {
		return data.EngineTransition{}, err
	}
	recoveryRaw, _ := json.Marshal(spec.Recovery)
	common := data.EngineTransitionCommon{Engine: data.EngineSpacetimeDB, Scope: scope, Base: base, NewEpochID: a.IncomingEpochID, FenceID: a.FenceID, FenceOwnerOperationID: a.FenceOwnerOpID, PreparedPoint: ref(data.EnginePreparedPointEvidence, a.PreparedPointID, spec.Safety.PointSHA256, a.SourceEpochID), SourceWitness: ref(data.EngineSourceWitnessEvidence, a.PreparedPointID, spec.Safety.SourceWitnessSHA256, a.SourceEpochID), OldTree: r.OldTree, TargetTree: r.TargetTree, PreservedParent: r.TargetTree.Parent, TargetVerification: ref(data.EngineTargetEvidence, r.ID, target.hash, a.IncomingEpochID), TargetRuntimeSHA256: r.RuntimeSHA256, TargetCheckpoint: ref(data.EngineCheckpointEvidence, r.ID, r.CheckpointSHA256, a.IncomingEpochID), TargetMaterial: ref(data.EngineMaterialEvidence, r.ID, r.MaterialSHA256, a.IncomingEpochID), RecoveryRelease: ref(data.EngineRecoveryEvidence, a.PlanID, nativePointHash(recoveryRaw), a.IncomingEpochID), RetainedDependencies: ref(data.EngineDependenciesEvidence, a.SelectedPointID, spec.Selected.DependenciesSHA256, a.IncomingEpochID)}
	return data.NewEngineRestoreTransition(common, data.EngineRestoreTarget{SelectedPoint: ref(data.EngineRestorePointEvidence, a.SelectedPointID, spec.Selected.PointSHA256, spec.Selected.EpochID)})
}

func (s *Store) ReadNativeRestoreTransition(ctx context.Context, capability *NativeRestoreTransition) (NativeRestoreTransitionView, error) {
	if !capability.active(s) {
		return NativeRestoreTransitionView{}, ErrConflict
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return NativeRestoreTransitionView{}, err
	}
	defer func() { _ = tx.Rollback() }()
	_, target, input, cursor, err := s.readNativeRestoreTransitionTx(ctx, tx, capability)
	if err != nil {
		return NativeRestoreTransitionView{}, err
	}
	view := NativeRestoreTransitionView{Input: input, Cursor: cursor, Runtime: target.runtime.Clone(), Allocation: cloneNativeAllocation(target.allocation)}
	return view, tx.Commit()
}

func (s *Store) readNativeRestoreTransitionTx(ctx context.Context, tx *sql.Tx, capability *NativeRestoreTransition) (nativeLiveRestoreAttempt, nativeRestoreTarget, data.EngineTransition, data.EngineTransitionCursor, error) {
	var retained nativeLiveRestoreAttempt
	var target nativeRestoreTarget
	var input data.EngineTransition
	var cursor data.EngineTransitionCursor
	if !capability.active(s) {
		return retained, target, input, cursor, ErrConflict
	}
	retained, err := readNativeLiveRestoreAttempt(ctx, tx, capability.attempt.OperationID)
	if err != nil || retained.claim.Attempt() != capability.attempt {
		return retained, target, input, cursor, errors.Join(ErrConflict, err)
	}
	reserved, bindingErr := readReservedEngine(ctx, tx, capability.attempt.BindingID)
	if bindingErr != nil || reserved.Database != capability.binding {
		return retained, target, input, cursor, errors.Join(ErrConflict, bindingErr)
	}
	if err = checkNativeRestoreTransitionClaimTx(ctx, tx, capability, retained); err != nil {
		return retained, target, input, cursor, err
	}
	target, err = readNativeRestoreTargetTx(ctx, tx, retained)
	if err != nil {
		return retained, target, input, cursor, err
	}
	expected, err := nativeRestoreTransitionInput(retained, target)
	if err != nil {
		return retained, target, input, cursor, err
	}
	input, cursor, err = readNativeRestoreTransitionInput(ctx, tx, expected, retained.current.Stage())
	if err != nil || input.SHA256() != expected.SHA256() || input.SHA256() != capability.inputSHA256 {
		return retained, target, input, cursor, errors.Join(&IntegrityError{}, err)
	}
	if cursor.Sequence == 0 {
		point, pointErr := readNativePreparedPointTx(ctx, tx, capability.attempt.PreparedPointID)
		if pointErr != nil || checkNativePointCurrent(ctx, tx, point.view) != nil {
			return retained, target, input, cursor, ErrConflict
		}
	}
	if err = checkEngineTransitionFence(ctx, tx, input); err != nil {
		return retained, target, input, cursor, err
	}
	if err = checkTransitionRuntimeStage(ctx, tx, input, cursor); err != nil {
		return retained, target, input, cursor, err
	}
	return retained, target, input, cursor, nil
}

func checkNativeRestoreTransitionClaimTx(ctx context.Context, tx *sql.Tx, capability *NativeRestoreTransition, retained nativeLiveRestoreAttempt) error {
	a, spec := retained.claim.Attempt(), retained.plan.Spec()
	point, err := readNativePreparedPointTx(ctx, tx, a.PreparedPointID)
	if err != nil || point.hash != spec.Safety.PointSHA256 || point.view.FenceID != a.FenceID || point.view.FenceOwnerOpID != a.FenceOwnerOpID || point.view.SourceWitnessSHA256 != spec.Safety.SourceWitnessSHA256 || point.view.StopProofSHA256 != spec.Safety.StopProofSHA256 || point.view.CheckpointSHA256 != spec.Safety.CheckpointSHA256 || point.view.SourceSealSHA256 != spec.Safety.SourceSealSHA256 {
		return errors.Join(ErrConflict, err)
	}
	consumer := nativePointConsumer{kind: nativePointLiveRestore, pointID: a.PreparedPointID, planID: a.PlanID, applyOperationID: a.OperationID, requester: spec.Requester.String(), binding: a.BindingID, epoch: a.SourceEpochID, generation: spec.Facts.Generation, generationID: spec.Facts.GenerationID, policySHA256: spec.Facts.PolicySHA256}
	wanted, err := consumer.canonical()
	if err != nil {
		return err
	}
	actual, err := readNativePointConsumer(ctx, tx, a.PreparedPointID)
	if err != nil || !bytes.Equal(actual, wanted) || nativePointHash(actual) != a.ConsumerSHA256 {
		return errors.Join(ErrConflict, err)
	}
	op, err := scanOperation(tx.QueryRowContext(ctx, "SELECT id,COALESCE(plan_id,''),requester,idempotency_key,state,created_at,updated_at,kind,app,secret_ref,recovery_of FROM operations WHERE id=?", a.OperationID))
	if err != nil || op.Kind != ops.NativeLiveRestore || op.SecretRef != a.PlanID || op.Requester != spec.Requester.String() || op.App != spec.Facts.App {
		return errors.Join(ErrConflict, err)
	}
	if capability.inspectOriginal {
		if op.State != ops.RecoveryRequired {
			return ErrConflict
		}
	} else if capability.resolveOperationID == "" {
		if op.State != ops.Preflight {
			return ErrConflict
		}
	} else if err = checkNativeRestoreResolverTx(ctx, tx, capability.resolveOperationID, op); err != nil {
		return err
	}
	var policy string
	err = tx.QueryRowContext(ctx, "SELECT policy_hash FROM data_admissions WHERE database_id=? ORDER BY version DESC LIMIT 1", spec.Facts.DatabaseID).Scan(&policy)
	if err != nil || strings.TrimPrefix(policy, "sha256:") != spec.Facts.PolicySHA256 {
		return errors.Join(ErrConflict, err)
	}
	bundle, err := readNativeVerifiedPointBundleTx(ctx, tx, a.SelectedPointID)
	if err != nil || nativeLiveRestoreSelected(bundle) != spec.Selected {
		return errors.Join(ErrConflict, err)
	}
	recovery, err := readNativeLiveRestoreRecoveryTx(ctx, tx, spec.Facts.App, spec.Recovery.ReleaseID, spec.Selected)
	if err != nil || recovery != spec.Recovery {
		return errors.Join(ErrConflict, err)
	}
	incoming, err := readNativeRepositoryIntentByOperation(ctx, tx, a.OperationID)
	if err != nil || incoming.record.ConsumerSHA256 != a.ConsumerSHA256 || incoming.epochID != a.IncomingEpochID || incoming.record.FenceID != a.FenceID || incoming.record.FenceOwnerOpID != a.FenceOwnerOpID {
		return errors.Join(ErrConflict, err)
	}
	return nil
}

func checkNativeRestoreResolverTx(ctx context.Context, tx *sql.Tx, id string, original ops.Operation) error {
	return checkNativeRestoreResolutionOperation(ctx, tx, id, original)
}
