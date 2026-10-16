package store

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"maps"
	"time"

	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/nativecapture"
	"github.com/ShaulLavo/brine/internal/ops"
)

type nativePreparedFrontendStopRecord struct {
	PointID     string    `json:"point_id"`
	OperationID string    `json:"operation_id"`
	ReleaseID   string    `json:"release_id"`
	Unit        string    `json:"unit"`
	UnitSHA     string    `json:"unit_sha256"`
	At          time.Time `json:"observed_at"`
}

func nativeUnstoppedIdentity(ctx context.Context, q dataQuerier, intent nativePreparationIntent, op ops.Operation) ([]byte, error) {
	if intent.SourceKind != "initialized" {
		return nil, ErrConflict
	}
	var facts []byte
	var prior NativePreparedRoutes
	for _, stage := range []string{"intent", "withdrawn"} {
		var raw []byte
		var r NativePreparedRoutes
		if err := q.QueryRowContext(ctx, "SELECT canonical FROM data_native_prepared_routes WHERE point_id=? AND stage=?", intent.PointID, stage).Scan(&raw); err != nil {
			return nil, err
		}
		if json.Unmarshal(raw, &r) != nil || r.PointID != intent.PointID || r.OperationID != op.ID || r.App != op.App || r.RecordedAt.Before(intent.CreatedAt) || r.RecordedAt.After(time.Now().UTC()) {
			return nil, ErrConflict
		}
		canonical, _ := json.Marshal(r)
		if !bytes.Equal(raw, canonical) || r.PreviousFiles == nil || r.CurrentFiles == nil {
			return nil, &IntegrityError{}
		}
		if stage == "withdrawn" && (r.PreviousGeneration != prior.PreviousGeneration || !maps.Equal(r.PreviousFiles, prior.PreviousFiles)) {
			return nil, ErrConflict
		}
		prior = r
		facts = append(facts, raw...)
	}
	wanted := maps.Clone(prior.PreviousFiles)
	delete(wanted, op.App+".caddy")
	if !maps.Equal(wanted, prior.CurrentFiles) || prior.CurrentGeneration < prior.PreviousGeneration {
		return nil, ErrConflict
	}

	_, frontend, _, err := nativeUnstoppedFrontend(ctx, q, intent, op)
	if err != nil {
		return nil, err
	}
	return append(facts, frontend...), nil
}

func nativeUnstoppedFrontend(ctx context.Context, q dataQuerier, intent nativePreparationIntent, op ops.Operation) (Release, []byte, bool, error) {
	var count int
	if err := q.QueryRowContext(ctx, "SELECT count(*) FROM data_native_prepared_frontend_stops WHERE point_id=?", intent.PointID).Scan(&count); err != nil {
		return Release{}, nil, false, err
	}
	if count == 0 {
		release, err := nativeUnstoppedOriginalRelease(ctx, q, intent, op.App)
		if err != nil {
			return Release{}, nil, false, err
		}
		if err = checkNativePreparedRelease(ctx, q, intent, release); err != nil {
			return Release{}, nil, false, err
		}
		raw, _ := json.Marshal(struct{ ReleaseID, FrontendStop string }{release.ID, "not_issued"})
		return release, raw, false, nil
	}

	if count != 2 {
		return Release{}, nil, false, ErrConflict
	}
	var facts []byte
	var previous nativePreparedFrontendStopRecord
	for _, stage := range []string{"intent", "stopped"} {
		var raw []byte
		var f nativePreparedFrontendStopRecord
		if err := q.QueryRowContext(ctx, "SELECT canonical FROM data_native_prepared_frontend_stops WHERE point_id=? AND stage=?", intent.PointID, stage).Scan(&raw); err != nil {
			return Release{}, nil, false, err
		}
		if json.Unmarshal(raw, &f) != nil || f.PointID != intent.PointID || f.OperationID != op.ID || f.ReleaseID == "" || f.Unit != op.App+".container" || f.At.Before(intent.CreatedAt) || f.At.After(time.Now().UTC()) {
			return Release{}, nil, false, ErrConflict
		}
		canonical, _ := json.Marshal(f)
		if !bytes.Equal(raw, canonical) {
			return Release{}, nil, false, &IntegrityError{}
		}
		if stage == "stopped" && (f.ReleaseID != previous.ReleaseID || f.Unit != previous.Unit || f.UnitSHA != previous.UnitSHA || f.At.Before(previous.At)) {
			return Release{}, nil, false, ErrConflict
		}
		previous = f
		facts = append(facts, raw...)
	}
	release, err := readRelease(ctx, q, op.App, previous.ReleaseID)
	if err != nil || checkNativePreparedRelease(ctx, q, intent, release) != nil || release.Managed.Frontend == nil || release.Managed.Frontend.Unit.Name != previous.Unit || release.Managed.Frontend.Unit.Hash != previous.UnitSHA {
		return Release{}, nil, false, ErrConflict
	}
	return release, facts, true, nil
}

func nativeUnstoppedCurrent(ctx context.Context, tx *sql.Tx, intent nativePreparationIntent, op ops.Operation) (Release, ManagedDesiredState, ManagedRouteState, error) {
	var zero Release
	if _, err := nativeUnstoppedIdentity(ctx, tx, intent, op); err != nil {
		return zero, ManagedDesiredState{}, ManagedRouteState{}, err
	}
	var releaseID string
	if err := tx.QueryRowContext(ctx, "SELECT current_id FROM release_heads WHERE app=?", op.App).Scan(&releaseID); err != nil {
		return zero, ManagedDesiredState{}, ManagedRouteState{}, err
	}
	release, err := readRelease(ctx, tx, op.App, releaseID)
	if err != nil {
		return zero, ManagedDesiredState{}, ManagedRouteState{}, err
	}
	recorded, _, _, err := nativeUnstoppedFrontend(ctx, tx, intent, op)
	if err != nil || recorded.ID != release.ID {
		return zero, ManagedDesiredState{}, ManagedRouteState{}, ErrConflict
	}
	desired, err := readManagedDesired(ctx, tx, intent.BindingID)
	if err != nil || !desired.Current(intent.BindingID, intent.EpochID, intent.RuntimeSHA256) {
		return zero, desired, ManagedRouteState{}, ErrConflict
	}
	route, err := readManagedRouteState(ctx, tx, op.App)
	if err != nil || route.BindingID != intent.BindingID || route.EpochID != intent.EpochID || route.RuntimeSHA256 != intent.RuntimeSHA256 {
		return zero, desired, route, ErrConflict
	}
	return release, desired, route, nil
}

// ReleaseUnstoppedPreparation records disposition without inventing a clean stop
// or backup point. Ordinary start owns the later component and ingress changes.
func (i *NativeBackupInspection) ReleaseUnstoppedPreparation(ctx context.Context, observationSHA256 string, engineRunning, frontendRunning bool, rootSHA256, loadedSHA256 string) (nativecapture.BackupResolutionReceipt, error) {
	var zero nativecapture.BackupResolutionReceipt
	if i.resolver == "" || observationSHA256 != i.review.ObservationSHA256 || !data.ValidCatalogHash(rootSHA256) || !data.ValidCatalogHash(loadedSHA256) {
		return zero, ErrConflict
	}
	expected, err := i.PreparedExpectation(ctx)
	if err != nil || !expected.StopNotIssued || expected.FrontendStopped && frontendRunning || frontendRunning && !engineRunning {
		return zero, ErrConflict
	}
	tx, cancel, err := i.state.beginWrite(ctx)
	defer cancel()
	if err != nil {
		return zero, err
	}
	defer func() { _ = tx.Rollback() }()
	intent, raw, err := readNativePreparationIntent(ctx, tx, expected.PointID)
	if err != nil {
		return zero, err
	}
	capability := &NativePreparation{store: i.state, preparation: &EnginePreparation{root: i.state.dir, binding: i.binding, guard: i.guard}, intent: intent, raw: raw, inspection: i}
	if err = checkNativePreparationCapability(ctx, tx, capability); err != nil {
		return zero, err
	}
	release, desired, route, err := nativeUnstoppedCurrent(ctx, tx, intent, i.original)
	if err != nil || release.ID != expected.Release.ID || desired != expected.Desired || route != expected.Route {
		return zero, ErrConflict
	}
	events, err := readNativePreparationEvents(ctx, tx, intent, raw)
	if err != nil || len(events) != 0 {
		return zero, ErrConflict
	}
	wanted := ManagedDesiredState{BindingID: i.binding, EpochID: intent.EpochID, RuntimeSHA256: intent.RuntimeSHA256, Revision: desired.Revision + 1, PlanID: release.PlanID, OperationID: i.resolver, EngineRunning: engineRunning, FrontendRunning: frontendRunning}
	next := route
	next.Revision++
	next.DesiredRevision = wanted.Revision
	next.PlanID = release.PlanID
	next.OperationID = i.resolver
	next.Generation = expected.Routes.CurrentGeneration
	next.Present = false
	next.FileHash = ""
	next.RootSHA256 = rootSHA256
	next.LoadedSHA256 = loadedSHA256
	if !wanted.Valid() || !next.Valid() {
		return zero, ErrConflict
	}
	wantedRaw, _ := json.Marshal(wanted)
	routeRaw, _ := json.Marshal(next)
	if _, err = tx.ExecContext(ctx, "INSERT INTO data_engine_desired_states VALUES(?,?,?,?,?)", i.binding, wanted.Revision, wanted.PlanID, i.resolver, wantedRaw); err != nil {
		return zero, err
	}
	if _, err = tx.ExecContext(ctx, "UPDATE data_engine_desired_heads SET revision=? WHERE binding_id=? AND revision=?", wanted.Revision, i.binding, desired.Revision); err != nil {
		return zero, err
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO data_engine_route_states VALUES(?,?,?,?,?,?)", next.App, i.binding, next.Revision, next.PlanID, i.resolver, routeRaw); err != nil {
		return zero, err
	}
	if _, err = tx.ExecContext(ctx, "UPDATE data_engine_route_heads SET revision=? WHERE app=? AND revision=?", next.Revision, next.App, route.Revision); err != nil {
		return zero, err
	}
	changed, err := tx.ExecContext(ctx, "UPDATE data_fences SET state='released' WHERE id=? AND operation_id=? AND database_id=? AND state='held'", intent.FenceID, intent.FenceOwnerOpID, intent.DatabaseID)
	if err != nil {
		return zero, err
	}
	n, err := changed.RowsAffected()
	if err != nil || n != 1 {
		return zero, errors.Join(ErrConflict, err)
	}
	disposition := nativecapture.PreparationReleaseReceipt{Kind: "native_prepare_release", OperationID: i.original.ID, BindingID: string(i.binding), EpochID: string(intent.EpochID), PointID: intent.PointID, FenceID: string(intent.FenceID), DesiredRevision: wanted.Revision, RouteRevision: next.Revision, Outcome: "released"}
	receiptRaw, _ := json.Marshal(disposition)
	receipt := nativecapture.BackupResolutionReceipt{Kind: string(ops.NativeBackupResolve), OperationID: i.resolver, ReviewID: i.review.ID, OriginalOperationID: i.original.ID, OriginalKind: string(i.original.Kind), BindingID: string(i.binding), EpochID: string(intent.EpochID), OriginalSHA256: i.originalSHA256, ObservationSHA256: observationSHA256, CompletionSHA256: nativePointHash(receiptRaw), Receipt: receiptRaw, Outcome: "released"}
	completed, _ := json.Marshal(receipt)
	if _, err = nativecapture.DecodeBackupResolutionReceipt(completed); err != nil {
		return zero, err
	}
	for _, stage := range []string{"observed", "completed"} {
		if _, err = tx.ExecContext(ctx, "INSERT INTO data_native_backup_resolution_events VALUES(?,?,?)", i.resolver, stage, completed); err != nil {
			return zero, err
		}
	}
	if err = completeTaskTx(ctx, tx, i.resolver, ops.Succeeded, ops.TaskOutcome{Receipt: completed}); err != nil {
		return zero, err
	}
	return receipt, tx.Commit()
}

func checkNativeReleasedPreparation(ctx context.Context, q dataQuerier, original ops.Operation, r nativecapture.PreparationReleaseReceipt) error {
	if original.Kind != ops.NativePrepare || r.OperationID != original.ID {
		return ErrConflict
	}
	intent, raw, err := readNativePreparationIntent(ctx, q, r.PointID)
	if err != nil || intent.OperationID != original.ID || string(intent.BindingID) != r.BindingID || string(intent.EpochID) != r.EpochID || string(intent.FenceID) != r.FenceID {
		return ErrConflict
	}
	events, err := readNativePreparationEvents(ctx, q, intent, raw)
	if err != nil || len(events) != 0 {
		return ErrConflict
	}
	if _, err = nativeUnstoppedIdentity(ctx, q, intent, original); err != nil {
		return err
	}
	var fence data.FenceState
	if err = q.QueryRowContext(ctx, "SELECT state FROM data_fences WHERE id=? AND operation_id=?", intent.FenceID, intent.FenceOwnerOpID).Scan(&fence); err != nil || fence != data.FenceReleased {
		return ErrConflict
	}
	var desiredRaw, routeRaw []byte
	var desired ManagedDesiredState
	var route ManagedRouteState
	if err = q.QueryRowContext(ctx, "SELECT canonical FROM data_engine_desired_states WHERE binding_id=? AND revision=?", intent.BindingID, r.DesiredRevision).Scan(&desiredRaw); err != nil {
		return err
	}
	if err = q.QueryRowContext(ctx, "SELECT canonical FROM data_engine_route_states WHERE app=? AND revision=?", original.App, r.RouteRevision).Scan(&routeRaw); err != nil {
		return err
	}
	if json.Unmarshal(desiredRaw, &desired) != nil || json.Unmarshal(routeRaw, &route) != nil || !desired.Valid() || !route.Valid() || desired.IngressServing || route.Present || route.DesiredRevision != desired.Revision || desired.EpochID != intent.EpochID || desired.RuntimeSHA256 != intent.RuntimeSHA256 || route.EpochID != intent.EpochID || route.RuntimeSHA256 != intent.RuntimeSHA256 || route.OperationID != desired.OperationID {
		return ErrConflict
	}
	_, _, frontendStopped, err := nativeUnstoppedFrontend(ctx, q, intent, original)
	if err != nil || frontendStopped && desired.FrontendRunning {
		return ErrConflict
	}
	resolver, err := nativeResolutionOperation(ctx, q, desired.OperationID)
	if err != nil || resolver.Kind != ops.NativeBackupResolve || resolver.State != ops.Succeeded || resolver.RecoveryOf != original.ID || resolver.Requester != original.Requester || resolver.App != original.App {
		return ErrConflict
	}
	return nil
}

func nativeUnstoppedOriginalRelease(ctx context.Context, q dataQuerier, intent nativePreparationIntent, app string) (Release, error) {
	rows, err := q.QueryContext(ctx, "SELECT id,committed_at FROM releases WHERE app=? ORDER BY rowid DESC", app)
	if err != nil {
		return Release{}, err
	}
	defer func() { _ = rows.Close() }()
	var selected string
	var latest time.Time
	for rows.Next() {
		var id, raw string
		if err := rows.Scan(&id, &raw); err != nil {
			return Release{}, err
		}
		committed, err := time.Parse(time.RFC3339Nano, raw)
		if err != nil {
			return Release{}, &IntegrityError{}
		}
		if committed.After(intent.CreatedAt) || !committed.After(latest) {
			continue
		}
		selected, latest = id, committed
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return Release{}, err
	}
	if selected == "" {
		return Release{}, ErrConflict
	}
	return readRelease(ctx, q, app, selected)
}
