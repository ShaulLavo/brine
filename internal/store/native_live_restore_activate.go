package store

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/liverestore"
)

// NativeRestoreTransitionObservation is freshly read by the trusted host while
// both real guards remain held. Callers cannot send it through the machine API.
type NativeRestoreTransitionObservation struct {
	Layout                      data.EngineLayoutObservation
	UnitSHA256                  string
	MaterialSHA256              string
	RecoveryCodeSHA256          string
	RecoveryCompatibilitySHA256 string
	EngineAbsent                bool
	ApplicationAbsent           bool
	IngressClosed               bool
	PrivateReadersAbsent        bool
	ObservedAt                  time.Time
}

func (NativeRestoreTransitionObservation) MarshalJSON() ([]byte, error) { return nil, ErrInvalid }

func checkNativeRestoreTransitionObservation(retained nativeLiveRestoreAttempt, target nativeRestoreTarget, input data.EngineTransition, observed NativeRestoreTransitionObservation) error {
	if data.ClassifyEngineTransitionLayout(input, observed.Layout) != data.EngineLayoutPromoted || observed.UnitSHA256 != target.runtime.UnitSHA256 || observed.MaterialSHA256 != target.record.MaterialSHA256 || !observed.EngineAbsent || !observed.ApplicationAbsent || !observed.IngressClosed || !observed.PrivateReadersAbsent || observed.ObservedAt.IsZero() || observed.ObservedAt.Location() != time.UTC || observed.ObservedAt.Before(target.record.ObservedAt) || observed.ObservedAt.After(time.Now().UTC()) {
		return ErrConflict
	}
	if observed.RecoveryCodeSHA256 != retained.plan.Spec().Recovery.CodeSHA256 || observed.RecoveryCompatibilitySHA256 != retained.plan.Spec().Recovery.CompatibilitySHA256 {
		return ErrConflict
	}
	return nil
}

func (s *Store) AdvanceNativeRestoreTransition(ctx context.Context, capability *NativeRestoreTransition, next data.EngineTransitionStage, observed data.EngineLayoutObservation) (data.EngineTransitionCursor, error) {
	var zero data.EngineTransitionCursor
	if !capability.active(s) {
		return zero, ErrConflict
	}
	switch next {
	case data.EngineTransitionPreserveIntent, data.EngineTransitionOriginalPreserved, data.EngineTransitionPromoteIntent, data.EngineTransitionTargetPromoted, data.EngineTransitionActivationIntent:
	default:
		return zero, ErrInvalid
	}
	tx, cancel, err := s.beginWrite(ctx)
	defer cancel()
	if err != nil {
		return zero, err
	}
	defer func() { _ = tx.Rollback() }()
	retained, _, input, cursor, err := s.readNativeRestoreTransitionTx(ctx, tx, capability)
	if err != nil || retained.current.Stage() != liverestore.TransitionIntent {
		return zero, errors.Join(ErrConflict, err)
	}
	event, err := data.AdvanceEngineTransition(input, cursor, next, observed)
	if err != nil {
		return zero, ErrConflict
	}
	if err = appendEngineTransitionEvent(ctx, tx, input, cursor, event); err != nil {
		return zero, err
	}
	if !capability.active(s) {
		return zero, ErrConflict
	}
	return event.Cursor(), tx.Commit()
}

func (s *Store) ActivateNativeLiveRestore(ctx context.Context, capability *NativeRestoreTransition, observed NativeRestoreTransitionObservation) (data.EngineTransitionCursor, error) {
	var zero data.EngineTransitionCursor
	if !capability.active(s) {
		return zero, ErrConflict
	}
	tx, cancel, err := s.beginWrite(ctx)
	defer cancel()
	if err != nil {
		return zero, err
	}
	defer func() { _ = tx.Rollback() }()
	retained, target, input, cursor, err := s.readNativeRestoreTransitionTx(ctx, tx, capability)
	if err != nil || retained.current.Stage() != liverestore.TransitionIntent || cursor.Stage != data.EngineTransitionActivationIntent {
		return zero, errors.Join(ErrConflict, err)
	}
	if err = checkNativeRestoreTransitionAnchors(ctx, capability, input, observed); err != nil {
		return zero, err
	}
	if err = checkNativeRestoreTransitionObservation(retained, target, input, observed); err != nil {
		return zero, err
	}
	if err = settleNativeRestoreCodeTx(ctx, tx, retained); err != nil {
		return zero, err
	}
	desired, effective, err := readEffectiveManagedDesired(ctx, tx, capability.attempt.BindingID, target.runtime.EpochID, target.record.RuntimeSHA256)
	if err != nil || effective && (desired.EngineRunning || desired.FrontendRunning || desired.IngressServing) {
		return zero, errors.Join(ErrConflict, err)
	}
	activated, err := s.activateEngineTransitionTx(ctx, tx, capability.preparation, cursor, target.runtime, target.allocation)
	if err != nil {
		return zero, err
	}
	next, err := liverestore.Settle(retained.plan, retained.current, liverestore.Transitioned, activated.EventSHA256)
	if err != nil {
		return zero, err
	}
	if err = s.appendNativeLiveRestoreEventTx(ctx, tx, capability.preparation, retained.current, next); err != nil {
		return zero, err
	}
	if !capability.active(s) {
		return zero, ErrConflict
	}
	return activated, tx.Commit()
}

func (s *Store) BeginNativeLiveRestoreCompletion(ctx context.Context, capability *NativeRestoreTransition, observed NativeRestoreTransitionObservation) (data.EngineTransitionCursor, error) {
	var zero data.EngineTransitionCursor
	if !capability.active(s) {
		return zero, ErrConflict
	}
	tx, cancel, err := s.beginWrite(ctx)
	defer cancel()
	if err != nil {
		return zero, err
	}
	defer func() { _ = tx.Rollback() }()
	retained, target, input, cursor, err := s.readNativeRestoreTransitionTx(ctx, tx, capability)
	if err != nil || retained.current.Stage() != liverestore.Transitioned || cursor.Stage != data.EngineTransitionActivated || retained.current.ProofSHA256() != cursor.EventSHA256 {
		return zero, errors.Join(ErrConflict, err)
	}
	if err = checkNativeRestoreTransitionAnchors(ctx, capability, input, observed); err != nil {
		return zero, err
	}
	if err = checkNativeRestoreTransitionObservation(retained, target, input, observed); err != nil {
		return zero, err
	}
	intent, err := liverestore.RecordIntent(retained.plan, retained.current, liverestore.FinishIntent)
	if err != nil {
		return zero, err
	}
	event, err := data.AdvanceEngineTransition(input, cursor, data.EngineTransitionFinishIntent, observed.Layout)
	if err != nil {
		return zero, err
	}
	if err = s.appendNativeLiveRestoreEventTx(ctx, tx, capability.preparation, retained.current, intent); err != nil {
		return zero, err
	}
	if err = appendEngineTransitionEvent(ctx, tx, input, cursor, event); err != nil {
		return zero, err
	}
	if !capability.active(s) {
		return zero, ErrConflict
	}
	return event.Cursor(), tx.Commit()
}

type nativeRestoreCompletionRecord struct {
	Format                      string              `json:"format"`
	ID                          string              `json:"id"`
	Attempt                     liverestore.Attempt `json:"attempt"`
	InputSHA256                 string              `json:"input_sha256"`
	TargetSHA256                string              `json:"target_sha256"`
	IntentSHA256                string              `json:"intent_sha256"`
	LayoutSHA256                string              `json:"layout_sha256"`
	UnitSHA256                  string              `json:"unit_sha256"`
	MaterialSHA256              string              `json:"material_sha256"`
	RecoveryCodeSHA256          string              `json:"recovery_code_sha256"`
	RecoveryCompatibilitySHA256 string              `json:"recovery_compatibility_sha256"`
	ObservedAt                  time.Time           `json:"observed_at"`
}

func (s *Store) CompleteNativeLiveRestore(ctx context.Context, capability *NativeRestoreTransition, observed NativeRestoreTransitionObservation) (liverestore.Event, error) {
	if !capability.active(s) {
		return liverestore.Event{}, ErrConflict
	}
	tx, cancel, err := s.beginWrite(ctx)
	defer cancel()
	if err != nil {
		return liverestore.Event{}, err
	}
	defer func() { _ = tx.Rollback() }()
	retained, target, input, cursor, err := s.readNativeRestoreTransitionTx(ctx, tx, capability)
	if err != nil || retained.current.Stage() != liverestore.FinishIntent || cursor.Stage != data.EngineTransitionFinishIntent {
		return liverestore.Event{}, errors.Join(ErrConflict, err)
	}
	if err = checkNativeRestoreTransitionAnchors(ctx, capability, input, observed); err != nil {
		return liverestore.Event{}, err
	}
	if err = checkNativeRestoreTransitionObservation(retained, target, input, observed); err != nil {
		return liverestore.Event{}, err
	}
	desired, effective, err := readEffectiveManagedDesired(ctx, tx, capability.attempt.BindingID, target.runtime.EpochID, target.record.RuntimeSHA256)
	if err != nil || effective && (desired.EngineRunning || desired.FrontendRunning || desired.IngressServing) {
		return liverestore.Event{}, errors.Join(ErrConflict, err)
	}
	id, err := data.NewID()
	if err != nil {
		return liverestore.Event{}, err
	}
	layoutRaw, _ := json.Marshal(observed.Layout)
	record := nativeRestoreCompletionRecord{Format: "brine-native-live-restore-completion-v1", ID: id, Attempt: capability.attempt, InputSHA256: input.SHA256(), TargetSHA256: target.hash, IntentSHA256: retained.current.SHA256(), LayoutSHA256: nativePointHash(layoutRaw), UnitSHA256: observed.UnitSHA256, MaterialSHA256: observed.MaterialSHA256, RecoveryCodeSHA256: observed.RecoveryCodeSHA256, RecoveryCompatibilitySHA256: observed.RecoveryCompatibilitySHA256, ObservedAt: observed.ObservedAt}
	raw, err := json.Marshal(record)
	if err != nil || len(raw) > 65536 {
		return liverestore.Event{}, ErrInvalid
	}
	_, err = tx.ExecContext(ctx, "INSERT INTO data_native_live_restore_completion_proofs VALUES(?,?,?)", id, capability.attempt.OperationID, raw)
	if err != nil {
		return liverestore.Event{}, err
	}
	hash := nativePointHash(raw)
	proof := data.EngineEvidenceRef{Kind: data.EngineCompletionEvidence, ID: id, SHA256: hash, DatabaseID: input.Common().Scope.DatabaseID, BindingID: capability.attempt.BindingID, EpochID: capability.attempt.IncomingEpochID}
	if _, err = s.finishEngineTransitionTx(ctx, tx, capability.preparation, cursor, proof, false); err != nil {
		return liverestore.Event{}, err
	}
	completed, err := liverestore.Settle(retained.plan, retained.current, liverestore.Completed, hash)
	if err != nil {
		return liverestore.Event{}, err
	}
	if err = s.appendNativeLiveRestoreEventTx(ctx, tx, capability.preparation, retained.current, completed); err != nil {
		return liverestore.Event{}, err
	}
	if !capability.active(s) {
		return liverestore.Event{}, ErrConflict
	}
	return completed, tx.Commit()
}
