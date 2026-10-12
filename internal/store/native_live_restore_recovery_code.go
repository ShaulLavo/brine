package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"github.com/ShaulLavo/brine/internal/plan"
	"github.com/ShaulLavo/brine/internal/policy"
)

// NativeRestoreRecoveryCode describes the reviewed historical code that the host
// must independently read back. It supplies no data or start permission.
type NativeRestoreRecoveryCode struct {
	App                 string
	Release             Release
	SourceRelease       Release
	Plan                plan.Plan
	Desired             policy.Desired
	CodeSHA256          string
	CompatibilitySHA256 string
}

func (s *Store) ReadNativeRestoreRecoveryCode(ctx context.Context, planID string) (NativeRestoreRecoveryCode, error) {
	var zero NativeRestoreRecoveryCode
	reviewed, err := s.LoadNativeLiveRestorePlan(ctx, planID)
	if err != nil {
		return zero, err
	}
	spec := reviewed.Spec()
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return zero, err
	}
	defer func() { _ = tx.Rollback() }()
	intent, err := readNativeRestoreCodeIntent(ctx, tx, reviewed)
	if err != nil {
		return zero, err
	}
	current, err := readRelease(ctx, tx, spec.Facts.App, spec.Recovery.ReleaseID)
	if err != nil || current.Managed == nil {
		return zero, ErrConflict
	}
	source, err := readRelease(ctx, tx, spec.Facts.App, intent.SourceReleaseID)
	if err != nil || source.Managed == nil {
		return zero, ErrConflict
	}
	p, desired, err := loadPlan(ctx, tx, current.PlanID)
	if err != nil || p.Managed == nil || desired.SpacetimeDB == nil || desired.SpacetimeDB.Module.Metadata == nil {
		return zero, ErrConflict
	}
	_, raw, err := normalizeRelease(current)
	if err != nil || nativePointHash(raw) != spec.Recovery.CodeSHA256 {
		return zero, ErrConflict
	}
	compatibility, err := json.Marshal(desired.SpacetimeDB.Module.Metadata)
	if err != nil || nativePointHash(compatibility) != spec.Recovery.CompatibilitySHA256 {
		return zero, ErrConflict
	}
	return NativeRestoreRecoveryCode{App: spec.Facts.App, Release: current, SourceRelease: source, Plan: p, Desired: desired, CodeSHA256: nativePointHash(raw), CompatibilitySHA256: nativePointHash(compatibility)}, tx.Commit()
}
