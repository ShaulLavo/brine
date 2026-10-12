package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/datainit"
	"github.com/ShaulLavo/brine/internal/liverestore"
	"github.com/ShaulLavo/brine/internal/policy"
)

func (s *Store) BuildNativeLiveRestorePlan(ctx context.Context, preparation *EnginePreparation, requester datainit.Requester, request liverestore.PlanRequest, pol policy.Policy) (liverestore.Plan, error) {
	if s.readOnly || preparation == nil || preparation.StateRoot() != s.dir || !requester.Valid() || !pol.AllowsLiveRestore() {
		return liverestore.Plan{}, ErrConflict
	}
	raw, err := json.Marshal(request)
	if err != nil {
		return liverestore.Plan{}, ErrInvalid
	}
	if _, err = liverestore.DecodePlanRequest(raw); err != nil {
		return liverestore.Plan{}, ErrInvalid
	}
	tx, cancel, err := s.beginWrite(ctx)
	defer cancel()
	if err != nil {
		return liverestore.Plan{}, err
	}
	defer func() { _ = tx.Rollback() }()
	point, err := readNativePreparedPointTx(ctx, tx, request.CurrentPreparedPointID)
	if err != nil || !preparation.Active(point.view.BindingID) || point.view.SourceKind != "initialized" {
		return liverestore.Plan{}, errors.Join(ErrConflict, err)
	}
	if err = checkNativePointCurrent(ctx, tx, point.view); err != nil {
		return liverestore.Plan{}, err
	}
	selected, err := readNativeVerifiedPointBundleTx(ctx, tx, request.SelectedPointID)
	if err != nil {
		return liverestore.Plan{}, err
	}
	facts, safety, err := readNativeLiveRestoreCurrentTx(ctx, tx, point)
	if err != nil || facts.App != request.App || facts.PolicySHA256 != strings.TrimPrefix(pol.Hash(), "sha256:") {
		return liverestore.Plan{}, errors.Join(ErrConflict, err)
	}
	reserved, err := readReservedEngine(ctx, tx, point.view.BindingID)
	if err != nil || string(reserved.Database.Name) != request.Database {
		return liverestore.Plan{}, errors.Join(ErrConflict, err)
	}
	selection := nativeLiveRestoreSelected(selected)
	if selection.NativeDatabase != point.view.NativeDatabase || selection.NativePublisher != point.view.NativePublisher {
		return liverestore.Plan{}, ErrConflict
	}
	recovery, err := readNativeLiveRestoreRecoveryTx(ctx, tx, request.App, request.RecoveryReleaseID, selection)
	if err != nil {
		return liverestore.Plan{}, err
	}
	now, expires := time.Now().UTC(), safety.ExpiresAt
	if limit := now.Add(liverestore.MaxReviewLifetime); limit.Before(expires) {
		expires = limit
	}
	plan, err := liverestore.NewPlan(liverestore.PlanSpec{Requester: requester, Facts: facts, Safety: safety, Selected: selection, Recovery: recovery, IssuedAt: now, ExpiresAt: expires})
	if err != nil {
		return liverestore.Plan{}, ErrConflict
	}
	if err = insertNativeLiveRestorePlanTx(ctx, tx, plan); err != nil || !preparation.Active(point.view.BindingID) {
		return liverestore.Plan{}, errors.Join(ErrConflict, err)
	}
	return plan, tx.Commit()
}

func readNativeLiveRestoreCurrentTx(ctx context.Context, tx *sql.Tx, point nativePreparedPoint) (liverestore.Facts, liverestore.Safety, error) {
	v := point.view
	reserved, err := readEnginePermit(ctx, tx, v.BindingID)
	if err != nil {
		return liverestore.Facts{}, liverestore.Safety{}, err
	}
	var app, policyHash string
	err = tx.QueryRowContext(ctx, "SELECT app FROM data_active_incarnations WHERE incarnation_id=?", v.IncarnationID).Scan(&app)
	if err == nil {
		err = tx.QueryRowContext(ctx, "SELECT policy_hash FROM data_admissions WHERE database_id=? ORDER BY version DESC LIMIT 1", v.DatabaseID).Scan(&policyHash)
	}
	if err != nil {
		return liverestore.Facts{}, liverestore.Safety{}, err
	}
	targetRaw, err := json.Marshal(struct {
		Binding    data.SpacetimeDBBinding `json:"binding"`
		Allocation *data.AllocationReceipt `json:"allocation"`
	}{reserved.Reserved.Database, reserved.Allocation})
	if err != nil {
		return liverestore.Facts{}, liverestore.Safety{}, ErrInvalid
	}
	desired, err := readManagedDesired(ctx, tx, v.BindingID)
	if err != nil && !errors.Is(err, ErrNotFound) {
		return liverestore.Facts{}, liverestore.Safety{}, err
	}
	var state *ManagedDesiredState
	if err == nil {
		state = &desired
	}
	desiredRaw, err := json.Marshal(struct {
		Format string               `json:"format"`
		State  *ManagedDesiredState `json:"state"`
	}{"brine-native-live-restore-desired-v1", state})
	if err != nil {
		return liverestore.Facts{}, liverestore.Safety{}, ErrInvalid
	}
	facts := liverestore.Facts{App: app, IncarnationID: v.IncarnationID, DatabaseID: v.DatabaseID, Head: data.EngineHeadRef{BindingID: v.BindingID, EpochID: v.EpochID, Version: v.RuntimeVersion, RuntimeSHA256: v.RuntimeSHA256}, Generation: v.Generation, GenerationID: v.GenerationID, PolicySHA256: strings.TrimPrefix(policyHash, "sha256:"), TargetSHA256: nativePointHash(targetRaw), DesiredSHA256: nativePointHash(desiredRaw)}
	safety := liverestore.Safety{PointID: v.PointID, PointSHA256: point.hash, FenceID: v.FenceID, FenceOwnerOpID: v.FenceOwnerOpID, SourceSealSHA256: v.SourceSealSHA256, SourceWitnessSHA256: v.SourceWitnessSHA256, StopProofSHA256: v.StopProofSHA256, CheckpointSHA256: v.CheckpointSHA256, PreparedAt: v.PreparedAt, ExpiresAt: v.ExpiresAt}
	return facts, safety, nil
}

func nativeLiveRestoreSelected(bundle nativeVerifiedPointBundle) liverestore.SelectedPoint {
	v, c := bundle.point.view, bundle.checkpoint.Record()
	return liverestore.SelectedPoint{IncarnationID: v.IncarnationID, DatabaseID: v.DatabaseID, BindingID: v.BindingID, PointID: v.PointID, PointSHA256: bundle.point.hash, EpochID: v.EpochID, GenerationID: v.GenerationID, CaptureID: bundle.transport.Identity.CaptureID, RepositoryID: v.RepositoryID, RepositorySnapshot: bundle.transport.SnapshotID, ManifestSHA256: v.ManifestSHA256, RestoreEvidenceSHA256: v.RestoreEvidenceSHA256, CheckpointSHA256: v.CheckpointSHA256, StateWitnessSHA256: v.SourceEvidenceSHA256, NativeDatabase: c.DatabaseIdentity, NativePublisher: c.PublisherIdentity, ProgramWireHash: c.RuntimeModuleHash, ArtifactWasmSHA256: c.ArtifactSHA256, SchemaSHA256: c.SchemaSHA256, MaterialSHA256: bundle.dependencies.signingSHA256, DependenciesSHA256: bundle.dependencies.hash, CommittedOffset: v.CommittedOffset, RecordedAt: v.VerifiedAt, RetainUntil: v.RetainUntil}
}

func readNativeLiveRestoreRecoveryTx(ctx context.Context, tx *sql.Tx, app, releaseID string, selected liverestore.SelectedPoint) (liverestore.RecoveryRelease, error) {
	release, err := readRelease(ctx, tx, app, releaseID)
	if err != nil || release.Managed == nil || release.Managed.BindingID != selected.BindingID {
		return liverestore.RecoveryRelease{}, errors.Join(ErrConflict, err)
	}
	_, desired, err := loadPlan(ctx, tx, release.PlanID)
	if err != nil || desired.SpacetimeDB == nil || desired.SpacetimeDB.Module.Metadata == nil {
		return liverestore.RecoveryRelease{}, errors.Join(ErrConflict, err)
	}
	metadata := desired.SpacetimeDB.Module.Metadata
	if string(metadata.RuntimeModuleHash) != selected.ProgramWireHash || string(metadata.WasmSHA256) != selected.ArtifactWasmSHA256 || metadata.SchemaSHA256 != selected.SchemaSHA256 {
		return liverestore.RecoveryRelease{}, ErrConflict
	}
	_, code, err := normalizeRelease(release)
	if err != nil {
		return liverestore.RecoveryRelease{}, err
	}
	compatibility, err := json.Marshal(metadata)
	if err != nil {
		return liverestore.RecoveryRelease{}, ErrInvalid
	}
	return liverestore.RecoveryRelease{ReleaseID: releaseID, CodeSHA256: nativePointHash(code), CompatibilitySHA256: nativePointHash(compatibility), ProgramWireHash: selected.ProgramWireHash, SchemaSHA256: selected.SchemaSHA256}, nil
}
