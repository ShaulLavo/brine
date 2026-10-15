package store

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/ops"
	"time"
)

type NativeBackupPreparedExpectation struct {
	Permit        EnginePermit
	Stop          NativeStopObservation
	Routes        NativePreparedRoutes
	PointID       string
	NeedsCapture  bool
	StopNotIssued bool
	Release       Release
	Desired       ManagedDesiredState
	Route         ManagedRouteState
	InitialOwner  *NativeInitialOwnerObservation
}

func (i *NativeBackupInspection) PreparedExpectation(ctx context.Context) (NativeBackupPreparedExpectation, error) {
	var zero NativeBackupPreparedExpectation
	if i.original.Kind != ops.NativePrepare {
		return zero, ErrConflict
	}
	if err := i.check(ctx, i.state.db); err != nil {
		return zero, err
	}
	var pointID string
	if err := i.state.db.QueryRowContext(ctx, "SELECT id FROM data_native_preparations WHERE operation_id=?", i.original.ID).Scan(&pointID); err != nil {
		return zero, ErrConflict
	}
	tx, err := i.state.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return zero, err
	}
	defer func() { _ = tx.Rollback() }()
	intent, raw, err := readNativePreparationIntent(ctx, tx, pointID)
	if err != nil || intent.OperationID != i.original.ID || intent.BindingID != i.binding {
		return zero, ErrConflict
	}
	events, err := readNativePreparationEvents(ctx, tx, intent, raw)
	if err != nil || len(events) != 0 && len(events) != 2 && len(events) != 6 {
		return zero, ErrConflict
	}
	var stopped NativeStopObservation
	if len(events) > 0 && (json.Unmarshal(events[1].Proof, &stopped) != nil || !validNativeStop(stopped, intent.SourceKind)) {
		return zero, &IntegrityError{}
	}
	var routeRaw []byte
	var routes NativePreparedRoutes
	err = tx.QueryRowContext(ctx, "SELECT canonical FROM data_native_prepared_routes WHERE point_id=? AND stage='withdrawn'", intent.PointID).Scan(&routeRaw)
	if err != nil || json.Unmarshal(routeRaw, &routes) != nil || routes.PointID != intent.PointID || routes.OperationID != i.original.ID || routes.App != i.original.App {
		return zero, ErrConflict
	}
	canonical, _ := json.Marshal(routes)
	if !bytes.Equal(canonical, routeRaw) {
		return zero, &IntegrityError{}
	}
	permit, err := readEnginePermit(ctx, tx, i.binding)
	if err != nil {
		return zero, err
	}
	capability := &NativePreparation{store: i.state, preparation: &EnginePreparation{root: i.state.dir, binding: i.binding, guard: i.guard}, intent: intent, raw: raw, inspection: i}
	if err = checkNativePreparationCapability(ctx, tx, capability); err != nil {
		return zero, err
	}
	if len(events) == 0 {
		release, desired, route, err := nativeUnstoppedCurrent(ctx, tx, intent, i.original)
		if err != nil {
			return zero, err
		}
		return NativeBackupPreparedExpectation{Permit: permit, Routes: routes, PointID: intent.PointID, StopNotIssued: true, Release: release, Desired: desired, Route: route}, tx.Commit()
	}
	needsCapture, err := nativePreparedNeedsCapture(ctx, tx, intent, events)
	if err != nil {
		return zero, err
	}
	var owner *NativeInitialOwnerObservation
	if needsCapture {
		owner, err = nativePreparedInitialOwner(ctx, tx, capability)
		if err != nil {
			return zero, err
		}
	}
	return NativeBackupPreparedExpectation{Permit: permit, Stop: stopped, Routes: routes, PointID: intent.PointID, NeedsCapture: needsCapture, InitialOwner: owner}, tx.Commit()
}
func (i *NativeBackupInspection) ResumePrepared(ctx context.Context, lifetime *data.EngineLock, current NativeStopObservation) (*NativeStoppedPreparation, error) {
	expected, err := i.PreparedExpectation(ctx)
	if err != nil || i.resolver == "" || !lifetime.HoldsLifetime(i.state.dir, i.binding) || !validNativeStop(current, string(expected.Permit.Runtime.State)) || current.Mode != expected.Stop.Mode || current.ContainerID != expected.Stop.ContainerID || current.Mode == "clean_exit" && !current.FinishedAt.Equal(expected.Stop.FinishedAt) || current.FinishedAt.After(time.Now().UTC()) {
		return nil, ErrConflict
	}
	intent, raw, err := readNativePreparationIntent(ctx, i.state.db, expected.PointID)
	if err != nil {
		return nil, err
	}
	stopRaw, _ := json.Marshal(expected.Stop)
	preparation := &NativePreparation{store: i.state, preparation: &EnginePreparation{root: i.state.dir, binding: i.binding, guard: i.guard}, intent: intent, raw: raw, inspection: i}
	return &NativeStoppedPreparation{preparation: preparation, lifetime: lifetime, stopSHA256: nativePointHash(stopRaw)}, nil
}
func checkNativeRetainedUploadObservation(ctx context.Context, q dataQuerier, operation string, observed NativePreparedObservation) error {
	var raw []byte
	var d nativeUploadDescriptor
	if err := q.QueryRowContext(ctx, "SELECT canonical FROM data_native_upload_descriptors WHERE operation_id=?", operation).Scan(&raw); err != nil || json.Unmarshal(raw, &d) != nil {
		return ErrConflict
	}
	source, _ := json.Marshal(observed.Source)
	if !bytes.Equal(d.Source, source) || !bytes.Equal(d.Manifest, observed.Manifest.Bytes()) || d.ArtifactSHA256 != nativePointHash(observed.Artifact) || d.DescriptorSHA256 != nativePointHash(observed.Descriptor) {
		return ErrConflict
	}
	if observed.Source.Kind() == "initialized" && (!bytes.Equal(d.SourceEvidence, observed.SourceEvidence.Bytes()) || !bytes.Equal(d.ConfirmedBound, observed.ConfirmedBound.Bytes()) || !bytes.Equal(d.FinalPairs, observed.FinalPairs.Bytes())) {
		return ErrConflict
	}
	expectedScope := d.Scope
	expectedScope.CredentialVersion = observed.Transport.Scope.CredentialVersion
	if !sameNativeResolutionScope(expectedScope, observed.Transport.Scope) {
		return ErrConflict
	}
	return nil
}

func nativePreparedOriginalIdentity(ctx context.Context, q dataQuerier, op ops.Operation) ([]byte, error) {
	var pointID string
	if err := q.QueryRowContext(ctx, "SELECT id FROM data_native_preparations WHERE operation_id=?", op.ID).Scan(&pointID); err != nil {
		return nil, ErrConflict
	}
	intent, raw, err := readNativePreparationIntent(ctx, q, pointID)
	if err != nil || intent.OperationID != op.ID {
		return nil, errors.Join(ErrConflict, err)
	}
	events, err := readNativePreparationEvents(ctx, q, intent, raw)
	if err == nil && len(events) == 0 {
		facts, err := nativeUnstoppedIdentity(ctx, q, intent, op)
		return bytes.Join([][]byte{raw, facts}, nil), err
	}
	if err != nil || len(events) != 2 && len(events) != 6 || checkNativePreparedStopFacts(intent, events[0].Proof, events[1].Proof) != nil {
		return nil, errors.Join(ErrConflict, err)
	}
	first, _ := json.Marshal(events[0])
	second, _ := json.Marshal(events[1])
	return bytes.Join([][]byte{raw, first, second}, nil), nil
}

func nativePreparedNeedsCapture(ctx context.Context, q dataQuerier, intent nativePreparationIntent, events []nativePreparationEvent) (bool, error) {
	var descriptors int
	if err := q.QueryRowContext(ctx, "SELECT count(*) FROM data_native_upload_descriptors WHERE operation_id=?", intent.OperationID).Scan(&descriptors); err != nil {
		return false, err
	}
	if descriptors == 1 {
		return false, nil
	}
	if descriptors != 0 || intent.SourceKind != "unpublished" || len(events) != 2 {
		return false, ErrConflict
	}
	var effects int
	err := q.QueryRowContext(ctx, `SELECT
 (SELECT count(*) FROM data_native_prepared_upload_intents WHERE point_id=?)+
 (SELECT count(*) FROM data_native_prepared_points WHERE id=?)+
 (SELECT count(*) FROM data_native_capture_inputs WHERE operation_id=?)`, intent.PointID, intent.PointID, intent.OperationID).Scan(&effects)
	if err != nil || effects != 0 {
		return false, errors.Join(ErrConflict, err)
	}
	return true, nil
}

func nativePreparedInitialOwner(ctx context.Context, tx *sql.Tx, preparation *NativePreparation) (*NativeInitialOwnerObservation, error) {
	var count int
	if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM data_native_initial_owners WHERE point_id=?", preparation.PointID()).Scan(&count); err != nil {
		return nil, err
	}
	if count == 0 {
		return nil, nil
	}
	if count != 1 {
		return nil, ErrConflict
	}
	owner, _, err := readNativeInitialOwner(ctx, tx, &NativeStoppedPreparation{preparation: preparation})
	if err != nil {
		return nil, err
	}
	return &owner, nil
}
