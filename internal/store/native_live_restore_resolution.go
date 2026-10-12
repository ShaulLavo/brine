package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/datainit"
	"github.com/ShaulLavo/brine/internal/liverestore"
	"github.com/ShaulLavo/brine/internal/ops"
	"github.com/ShaulLavo/brine/internal/policy"
	"time"
)

// The temporary inspection capability never leaves Store and never grants
// mutation authority. The returned view supplies expectations for host readback.
func (s *Store) inspectNativeRestoreTransitionTx(ctx context.Context, tx *sql.Tx, preparation *EnginePreparation, lifetime *data.EngineLock, originalID string) (*NativeRestoreTransition, nativeLiveRestoreAttempt, nativeRestoreTarget, data.EngineTransition, data.EngineTransitionCursor, error) {
	retained, err := readNativeLiveRestoreAttempt(ctx, tx, originalID)
	if err != nil {
		return nil, retained, nativeRestoreTarget{}, data.EngineTransition{}, data.EngineTransitionCursor{}, err
	}
	reserved, err := readReservedEngine(ctx, tx, retained.claim.Attempt().BindingID)
	if err != nil {
		return nil, retained, nativeRestoreTarget{}, data.EngineTransition{}, data.EngineTransitionCursor{}, err
	}
	input, _, err := readEngineTransition(ctx, tx, retained.claim.Attempt().TransitionID)
	if err != nil {
		return nil, retained, nativeRestoreTarget{}, data.EngineTransition{}, data.EngineTransitionCursor{}, err
	}
	cap := &NativeRestoreTransition{state: s, binding: reserved.Database, preparation: preparation, lifetime: lifetime, attempt: retained.claim.Attempt(), inputSHA256: input.SHA256(), inspectOriginal: true}
	retained, target, input, cursor, err := s.readNativeRestoreTransitionTx(ctx, tx, cap)
	return cap, retained, target, input, cursor, err
}

func (s *Store) ReadNativeRestoreResolutionTarget(ctx context.Context, preparation *EnginePreparation, lifetime *data.EngineLock, originalID string) (NativeRestoreTransitionView, error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return NativeRestoreTransitionView{}, err
	}
	defer func() { _ = tx.Rollback() }()
	_, _, target, input, cursor, err := s.inspectNativeRestoreTransitionTx(ctx, tx, preparation, lifetime, originalID)
	if err != nil {
		return NativeRestoreTransitionView{}, err
	}
	return NativeRestoreTransitionView{Input: input, Cursor: cursor, Runtime: target.runtime.Clone(), Allocation: cloneNativeAllocation(target.allocation)}, tx.Commit()
}

func nativeRestoreResolutionObservationHash(o NativeRestoreTransitionObservation) string {
	raw, _ := json.Marshal(struct {
		Layout        data.EngineLayoutObservation `json:"layout"`
		Unit          string                       `json:"unit"`
		Material      string                       `json:"material"`
		Code          string                       `json:"code"`
		Compatibility string                       `json:"compatibility"`
	}{o.Layout, o.UnitSHA256, o.MaterialSHA256, o.RecoveryCodeSHA256, o.RecoveryCompatibilitySHA256})
	return nativePointHash(raw)
}

func (s *Store) ReviewNativeRestoreResolution(ctx context.Context, preparation *EnginePreparation, lifetime *data.EngineLock, originalID string, requester datainit.Requester, pol policy.Policy, observed NativeRestoreTransitionObservation) (liverestore.Resolution, error) {
	var zero liverestore.Resolution
	if !requester.Valid() || !pol.AllowsLiveRestore() {
		return zero, ErrConflict
	}
	tx, cancel, err := s.beginWrite(ctx)
	defer cancel()
	if err != nil {
		return zero, err
	}
	defer func() { _ = tx.Rollback() }()
	cap, retained, target, input, _, err := s.inspectNativeRestoreTransitionTx(ctx, tx, preparation, lifetime, originalID)
	if err != nil || retained.plan.Spec().Requester != requester || pol.Hash() != "sha256:"+retained.plan.Spec().Facts.PolicySHA256 {
		return zero, ErrConflict
	}
	if err = checkNativeRestoreResumeObservation(ctx, cap, retained, target, input, observed); err != nil {
		return zero, err
	}
	now := time.Now().UTC()
	r := liverestore.Resolution{Format: "brine-native-live-restore-resolution-v1", OriginalOperationID: originalID, ApplyPlanID: retained.plan.ID(), Requester: requester, EventSHA256: retained.current.SHA256(), InputSHA256: input.SHA256(), ObservationSHA256: nativeRestoreResolutionObservationHash(observed), IssuedAt: now, ExpiresAt: now.Add(liverestore.MaxReviewLifetime)}
	raw, err := json.Marshal(r)
	if err != nil || r.ID() == "" {
		return zero, ErrInvalid
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO data_native_live_restore_resolutions VALUES(?,?,?)", r.ID(), originalID, raw); err != nil {
		return zero, err
	}
	return r, tx.Commit()
}

func readNativeRestoreResolution(ctx context.Context, q dataQuerier, id string) (liverestore.Resolution, error) {
	var raw []byte
	var original string
	if err := q.QueryRowContext(ctx, "SELECT operation_id,canonical FROM data_native_live_restore_resolutions WHERE id=?", id).Scan(&original, &raw); err != nil {
		return liverestore.Resolution{}, err
	}
	r, err := liverestore.DecodeResolution(raw)
	if err != nil || r.ID() != id || r.OriginalOperationID != original {
		return liverestore.Resolution{}, &IntegrityError{}
	}
	return r, nil
}
func (s *Store) ReadNativeRestoreResolution(ctx context.Context, id string) (liverestore.Resolution, error) {
	return readNativeRestoreResolution(ctx, s.db, id)
}

func checkNativeRestoreResumeObservation(ctx context.Context, cap *NativeRestoreTransition, retained nativeLiveRestoreAttempt, target nativeRestoreTarget, input data.EngineTransition, o NativeRestoreTransitionObservation) error {
	if data.ClassifyEngineTransitionLayout(input, o.Layout) == data.EngineLayoutConflict || !o.EngineAbsent || !o.ApplicationAbsent || !o.IngressClosed || !o.PrivateReadersAbsent || o.UnitSHA256 != target.runtime.UnitSHA256 || o.MaterialSHA256 != target.record.MaterialSHA256 || o.RecoveryCodeSHA256 != retained.plan.Spec().Recovery.CodeSHA256 || o.RecoveryCompatibilitySHA256 != retained.plan.Spec().Recovery.CompatibilitySHA256 || o.ObservedAt.IsZero() || o.ObservedAt.Location() != time.UTC || o.ObservedAt.Before(target.record.ObservedAt) || o.ObservedAt.After(time.Now().UTC()) {
		return ErrConflict
	}
	return checkNativeRestoreTransitionAnchors(ctx, cap, input, o)
}

func checkNativeRestoreResolutionOperation(ctx context.Context, tx *sql.Tx, id string, original ops.Operation) error {
	resolve, err := scanOperation(tx.QueryRowContext(ctx, "SELECT id,COALESCE(plan_id,''),requester,idempotency_key,state,created_at,updated_at,kind,app,secret_ref,recovery_of FROM operations WHERE id=?", id))
	if err != nil || original.State != ops.RecoveryRequired || resolve.Kind != ops.NativeLiveRestoreResolve || resolve.State != ops.Preflight || resolve.RecoveryOf != original.ID || resolve.Requester != original.Requester || resolve.App != original.App {
		return ErrConflict
	}
	r, err := readNativeRestoreResolution(ctx, tx, resolve.SecretRef)
	if err != nil || r.OriginalOperationID != original.ID || r.ApplyPlanID != original.SecretRef || r.Requester.String() != original.Requester {
		return ErrConflict
	}
	return nil
}
