package store

import (
	"context"
	"encoding/json"
	"github.com/ShaulLavo/brine/internal/plan"
	"github.com/ShaulLavo/brine/internal/policy"
)

// NativeRestoreRecoveryCode describes the retained current code that the host
// must independently read back. It supplies no data or start permission.
type NativeRestoreRecoveryCode struct {
	App                 string
	Release             Release
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
	current, err := s.CurrentRelease(ctx, spec.Facts.App)
	if err != nil || current.ID != spec.Recovery.ReleaseID || current.Managed == nil {
		return zero, ErrConflict
	}
	p, desired, err := s.LoadPlan(ctx, current.PlanID)
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
	return NativeRestoreRecoveryCode{App: spec.Facts.App, Release: current, Plan: p, Desired: desired, CodeSHA256: nativePointHash(raw), CompatibilitySHA256: nativePointHash(compatibility)}, nil
}
