package store

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/ops"
	"time"
)

type NativeBackupPreparedExpectation struct {
	Permit  EnginePermit
	Stop    NativeStopObservation
	Routes  NativePreparedRoutes
	PointID string
}

func (i *NativeBackupInspection) PreparedExpectation(ctx context.Context) (NativeBackupPreparedExpectation, error) {
	var zero NativeBackupPreparedExpectation
	if i.original.Kind != ops.NativePrepare {
		return zero, ErrConflict
	}
	if err := i.check(ctx, i.state.db); err != nil {
		return zero, err
	}
	retained, err := i.Upload(ctx)
	if err != nil {
		return zero, err
	}
	tx, err := i.state.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return zero, err
	}
	defer func() { _ = tx.Rollback() }()
	intent, raw, err := readNativePreparationIntent(ctx, tx, retained.Manifest.Identity().CaptureID)
	if err != nil || intent.OperationID != i.original.ID || intent.BindingID != i.binding {
		return zero, ErrConflict
	}
	events, err := readNativePreparationEvents(ctx, tx, intent, raw)
	if err != nil || len(events) != 2 && len(events) != 6 {
		return zero, ErrConflict
	}
	var stopped NativeStopObservation
	if json.Unmarshal(events[1].Proof, &stopped) != nil || !validNativeStop(stopped, intent.SourceKind) {
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
	return NativeBackupPreparedExpectation{Permit: permit, Stop: stopped, Routes: routes, PointID: intent.PointID}, tx.Commit()
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
	if !sameNativeResolutionScope(d.Scope, observed.Transport.Scope) {
		return ErrConflict
	}
	return nil
}
