package store

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"slices"
	"time"

	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/nativecapture"
	"github.com/ShaulLavo/brine/internal/ops"
	"github.com/ShaulLavo/brine/internal/policy"
)

type NativeAbandonment struct {
	state       *Store
	preparation *EnginePreparation
	lifetime    *data.EngineLock
	operation   ops.Operation
	facts       nativeAbandonFacts
}

type nativeAbandonFacts struct {
	input     NativeTaskInput
	point     nativePreparedPoint
	bundle    NativePreparedBundle
	permit    EnginePermit
	release   Release
	desired   policy.Desired
	wanted    ManagedDesiredState
	codeSHA   string
	routesSHA string
}

type NativeAbandonExpected struct {
	App        string
	Point      NativePreparedPointView
	Bundle     NativePreparedBundle
	Permit     EnginePermit
	Release    Release
	Desired    policy.Desired
	Wanted     ManagedDesiredState
	CodeSHA256 string
}

type NativeAbandonObservation struct {
	Stop          NativeStopObservation
	SourceSHA256  string
	CodeSHA256    string
	RoutesSHA256  string
	Directory     data.EngineDirectoryIdentity
	CheckpointSHA string
	ObservedAt    time.Time
}

func (NativeAbandonment) MarshalJSON() ([]byte, error)        { return nil, ErrInvalid }
func (NativeAbandonObservation) MarshalJSON() ([]byte, error) { return nil, ErrInvalid }

func (a *NativeAbandonment) active() bool {
	return a != nil && a.state != nil && !a.state.readOnly && a.preparation.Active(a.facts.point.view.BindingID) && a.preparation.StateRoot() == a.state.dir && a.lifetime.HoldsLifetime(a.state.dir, a.facts.point.view.BindingID)
}

func (a *NativeAbandonment) Close() error {
	if a == nil {
		return nil
	}
	var err error
	if a.lifetime != nil {
		err = a.lifetime.Release()
		a.lifetime = nil
	}
	if a.preparation != nil {
		err = errors.Join(err, a.preparation.Close())
		a.preparation = nil
	}
	return err
}

func (s *Store) OpenNativeAbandonment(ctx context.Context, operationID string) (_ *NativeAbandonment, err error) {
	if s.readOnly {
		return nil, ErrInvalid
	}
	op, err := s.GetOperation(ctx, operationID)
	if err != nil || op.Kind != ops.NativePrepareAbandon || op.State != ops.Preflight {
		return nil, errors.Join(ErrConflict, err)
	}
	facts, err := s.readNativeAbandonFacts(ctx, op)
	if err != nil {
		return nil, err
	}
	a := &NativeAbandonment{state: s, operation: op, facts: facts}
	complete := false
	defer func() {
		if !complete {
			err = errors.Join(err, a.Close())
		}
	}()
	p := facts.point.view
	a.preparation, err = s.AcquireEnginePreparation(ctx, p.BindingID, p.FenceID, p.FenceOwnerOpID)
	if err != nil {
		return nil, err
	}
	a.lifetime, err = data.AcquireEngineLifetimeLock(ctx, s.dir, p.BindingID)
	if err != nil {
		return nil, err
	}
	if _, err := a.Expected(ctx); err != nil {
		return nil, err
	}
	complete = true
	return a, nil
}

func (a *NativeAbandonment) Expected(ctx context.Context) (NativeAbandonExpected, error) {
	if !a.active() {
		return NativeAbandonExpected{}, ErrConflict
	}
	facts, err := a.state.readNativeAbandonFacts(ctx, a.operation)
	if err != nil || !sameNativeAbandonFacts(a.facts, facts) || !a.active() {
		return NativeAbandonExpected{}, errors.Join(ErrConflict, err)
	}
	return NativeAbandonExpected{App: a.operation.App, Point: facts.bundle.Point(), Bundle: facts.bundle, Permit: facts.permit, Release: facts.release, Desired: facts.desired, Wanted: facts.wanted, CodeSHA256: facts.codeSHA}, nil
}

func sameNativeAbandonFacts(before, after nativeAbandonFacts) bool {
	return before.point.hash == after.point.hash && before.codeSHA == after.codeSHA && before.routesSHA == after.routesSHA && before.wanted == after.wanted && before.release.ID == after.release.ID
}

func (s *Store) readNativeAbandonFacts(ctx context.Context, operation ops.Operation) (nativeAbandonFacts, error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nativeAbandonFacts{}, err
	}
	defer func() { _ = tx.Rollback() }()
	facts, err := readNativeAbandonFactsTx(ctx, tx, operation)
	if err != nil {
		return nativeAbandonFacts{}, err
	}
	return facts, tx.Commit()
}

func readNativeAbandonFactsTx(ctx context.Context, tx *sql.Tx, operation ops.Operation) (nativeAbandonFacts, error) {
	var facts nativeAbandonFacts
	input, err := readNativeTaskInput(ctx, tx, operation)
	if err != nil || operation.Kind != ops.NativePrepareAbandon || input.Kind != ops.NativePrepareAbandon {
		return facts, errors.Join(ErrConflict, err)
	}
	point, err := readNativePreparedPointTx(ctx, tx, input.PointID)
	if err != nil || point.view.SourceKind != "initialized" || point.view.BindingID != input.BindingID || point.view.FenceOwnerOpID != point.view.PreparationOperationID {
		return facts, errors.Join(ErrConflict, err)
	}
	if err := checkNativePointCurrent(ctx, tx, point.view); err != nil {
		return facts, err
	}
	if err := checkNativePreparedCurrentPolicy(ctx, tx, point.view); err != nil {
		return facts, err
	}
	if err := checkNativeAbandonOriginal(ctx, tx, operation, point); err != nil {
		return facts, err
	}
	if err := checkNativeAbandonUnused(ctx, tx, point.view); err != nil {
		return facts, err
	}
	if err := refusePendingNativeCaptureMutation(ctx, tx, input.BindingID, operation.ID); err != nil {
		return facts, err
	}
	if err := refusePendingNativePreparation(ctx, tx, input.BindingID, point.view.PreparationOperationID); err != nil {
		return facts, err
	}
	bundle, err := readNativePreparedBundleTx(ctx, tx, input.PointID)
	if err != nil {
		return facts, err
	}
	permit, err := readEnginePermit(ctx, tx, input.BindingID)
	if err != nil {
		return facts, err
	}
	release, desired, codeSHA, err := readNativeAbandonCode(ctx, tx, operation.App, point.view, permit.Runtime)
	if err != nil {
		return facts, err
	}
	wanted, err := readManagedDesired(ctx, tx, input.BindingID)
	if err != nil || !wanted.Current(input.BindingID, point.view.EpochID, point.view.RuntimeSHA256) {
		return facts, errors.Join(ErrConflict, err)
	}
	var routeRaw []byte
	if err := tx.QueryRowContext(ctx, "SELECT canonical FROM data_native_prepared_routes WHERE point_id=? AND stage='withdrawn'", point.view.PointID).Scan(&routeRaw); err != nil {
		return facts, errors.Join(ErrConflict, err)
	}
	var routes NativePreparedRoutes
	if len(routeRaw) == 0 || json.Unmarshal(routeRaw, &routes) != nil || routes.PointID != point.view.PointID || routes.OperationID != point.view.PreparationOperationID {
		return facts, &IntegrityError{}
	}
	canonicalRoutes, err := json.Marshal(routes)
	if err != nil || !bytes.Equal(routeRaw, canonicalRoutes) {
		return facts, &IntegrityError{}
	}
	return nativeAbandonFacts{input: input, point: point, bundle: bundle, permit: permit, release: release, desired: desired, wanted: wanted, codeSHA: codeSHA, routesSHA: nativePointHash(routeRaw)}, nil
}

func checkNativeAbandonOriginal(ctx context.Context, tx *sql.Tx, operation ops.Operation, point nativePreparedPoint) error {
	original, err := nativeResolutionOperation(ctx, tx, point.view.PreparationOperationID)
	if err != nil || original.Kind != ops.NativePrepare || original.State != ops.Succeeded || original.Requester != operation.Requester || original.App != operation.App {
		return errors.Join(ErrConflict, err)
	}
	var raw []byte
	if err := tx.QueryRowContext(ctx, "SELECT canonical FROM operation_outcomes WHERE operation_id=?", original.ID).Scan(&raw); err != nil {
		return err
	}
	outcome, err := ops.DecodeTaskOutcome(original, raw)
	if err != nil {
		return &IntegrityError{}
	}
	receipt, err := nativecapture.DecodeTaskReceipt(outcome.Receipt)
	p := point.view
	if err != nil || receipt.Kind != string(ops.NativePrepare) || receipt.OperationID != original.ID || receipt.PointID != p.PointID || receipt.PointSHA256 != point.hash || receipt.BindingID != string(p.BindingID) || receipt.EpochID != string(p.EpochID) {
		return &IntegrityError{}
	}
	return nil
}

func checkNativeAbandonUnused(ctx context.Context, tx *sql.Tx, point NativePreparedPointView) error {
	if err := refuseConsumedNativeFence(ctx, tx, point.FenceID); err != nil {
		return err
	}
	var count int
	err := tx.QueryRowContext(ctx, "SELECT count(*) FROM data_engine_transition_inputs WHERE fence_id=? OR prepared_point_id=?", point.FenceID, point.PointID).Scan(&count)
	if err != nil || count != 0 {
		return errors.Join(ErrConflict, err)
	}
	err = tx.QueryRowContext(ctx, "SELECT count(*) FROM data_fences WHERE database_id=? AND state='held'", point.DatabaseID).Scan(&count)
	if err != nil || count != 1 {
		return errors.Join(ErrConflict, err)
	}
	return nil
}

func readNativeAbandonCode(ctx context.Context, tx *sql.Tx, app string, point NativePreparedPointView, runtime data.EngineRuntime) (Release, policy.Desired, string, error) {
	var releaseID string
	if err := tx.QueryRowContext(ctx, "SELECT current_id FROM release_heads WHERE app=?", app).Scan(&releaseID); err != nil {
		return Release{}, policy.Desired{}, "", err
	}
	release, err := readRelease(ctx, tx, app, releaseID)
	if err != nil || release.Managed == nil || release.Managed.BindingID != point.BindingID {
		return Release{}, policy.Desired{}, "", errors.Join(ErrConflict, err)
	}
	planned, desired, err := loadPlan(ctx, tx, release.PlanID)
	if err != nil || planned.Managed == nil || planned.Managed.Database.IncarnationID != point.IncarnationID || desired.SpacetimeDB == nil || !slices.Contains(desired.SpacetimeDB.Module.Accepts, runtime.Module.Schema.Marker) {
		return Release{}, policy.Desired{}, "", errors.Join(ErrConflict, err)
	}
	_, raw, err := normalizeRelease(release)
	if err != nil {
		return Release{}, policy.Desired{}, "", err
	}
	return release, desired, nativePointHash(raw), nil
}
