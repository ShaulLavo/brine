package store

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/liverestore"
)

// ResumeNativeLiveRestoreTransition is deliberately independent of the expired
// stopped permit. It reopens only an already admitted original16 claim under a
// new resolver operation and freshly observed original/target layout and absence.
func (s *Store) ResumeNativeLiveRestoreTransition(ctx context.Context, preparation *EnginePreparation, lifetime *data.EngineLock, originalID, resolveID string, observed NativeRestoreTransitionObservation) (*NativeRestoreTransition, error) {
	if s.readOnly || preparation == nil || preparation.StateRoot() != s.dir || !preparation.Active(preparation.binding) || !lifetime.HoldsLifetime(s.dir, preparation.binding) || originalID == resolveID {
		return nil, ErrConflict
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	retained, err := readNativeLiveRestoreAttempt(ctx, tx, originalID)
	if err != nil || retained.claim.Attempt().BindingID != preparation.binding {
		return nil, errors.Join(ErrConflict, err)
	}
	switch retained.current.Stage() {
	case liverestore.TransitionIntent, liverestore.Transitioned, liverestore.FinishIntent:
	default:
		return nil, ErrConflict
	}
	input, _, err := readEngineTransition(ctx, tx, retained.claim.Attempt().TransitionID)
	if err != nil {
		return nil, err
	}
	reserved, err := readReservedEngine(ctx, tx, preparation.binding)
	if err != nil {
		return nil, err
	}
	capability := &NativeRestoreTransition{state: s, binding: reserved.Database, preparation: preparation, lifetime: lifetime, attempt: retained.claim.Attempt(), inputSHA256: input.SHA256(), resolveOperationID: resolveID}
	_, target, input, cursor, err := s.readNativeRestoreTransitionTx(ctx, tx, capability)
	if err != nil {
		return nil, err
	}
	layout := data.ClassifyEngineTransitionLayout(input, observed.Layout)
	if layout == data.EngineLayoutConflict || !observed.EngineAbsent || !observed.ApplicationAbsent || !observed.IngressClosed || !observed.PrivateReadersAbsent || observed.UnitSHA256 != target.runtime.UnitSHA256 || observed.MaterialSHA256 != target.record.MaterialSHA256 || observed.RecoveryCodeSHA256 != retained.plan.Spec().Recovery.CodeSHA256 || observed.RecoveryCompatibilitySHA256 != retained.plan.Spec().Recovery.CompatibilitySHA256 || observed.ObservedAt.IsZero() || observed.ObservedAt.Location() != time.UTC || observed.ObservedAt.Before(target.record.ObservedAt) || observed.ObservedAt.After(time.Now().UTC()) {
		return nil, ErrConflict
	}
	if cursor.Stage == data.EngineTransitionActivated || cursor.Stage == data.EngineTransitionFinishIntent {
		if layout != data.EngineLayoutPromoted {
			return nil, ErrConflict
		}
	}
	if !capability.active(s) {
		return nil, ErrConflict
	}
	return capability, tx.Commit()
}
