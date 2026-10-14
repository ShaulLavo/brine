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
	"github.com/ShaulLavo/brine/internal/strictjson"
)

type NativeAbandonment struct {
	state       *Store
	preparation *EnginePreparation
	lifetime    *data.EngineLock
	operation   ops.Operation
	facts       nativeAbandonFacts
}

type nativeAbandonFacts struct {
	input      NativeTaskInput
	point      nativePreparedPoint
	checkpoint nativecapture.Checkpoint
	permit     EnginePermit
	release    Release
	desired    policy.Desired
	wanted     ManagedDesiredState
	codeSHA    string
	routesSHA  string
}

type NativeAbandonExpected struct {
	App        string
	Point      NativePreparedPointView
	Checkpoint nativecapture.Checkpoint
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
	point := facts.point.view
	point.PointSHA256 = facts.point.hash
	point.Names = slices.Clone(point.Names)
	return NativeAbandonExpected{App: a.operation.App, Point: point, Checkpoint: facts.checkpoint, Permit: facts.permit, Release: facts.release, Desired: facts.desired, Wanted: facts.wanted, CodeSHA256: facts.codeSHA}, nil
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
	checkpoint, err := readNativeAbandonCheckpointTx(ctx, tx, point, operation)
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
	return nativeAbandonFacts{input: input, point: point, checkpoint: checkpoint, permit: permit, release: release, desired: desired, wanted: wanted, codeSHA: codeSHA, routesSHA: nativePointHash(routeRaw)}, nil
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

func readNativeAbandonCheckpointTx(ctx context.Context, tx *sql.Tx, point nativePreparedPoint, operation ops.Operation) (nativecapture.Checkpoint, error) {
	var raw, provenance, artifact, descriptor []byte
	var generation, operationID string
	p := point.view
	if err := tx.QueryRowContext(ctx, "SELECT generation_id,operation_id,canonical,provenance,artifact,descriptor FROM data_native_checkpoints WHERE binding_id=? AND epoch_id=? AND generation_id=?", p.BindingID, p.EpochID, p.GenerationID).Scan(&generation, &operationID, &raw, &provenance, &artifact, &descriptor); err != nil {
		return nativecapture.Checkpoint{}, err
	}
	checkpoint, err := nativecapture.DecodeCheckpoint(raw, nativecapture.DefaultLimits())
	if err != nil || checkpoint.SHA256() != p.CheckpointSHA256 || generation != p.GenerationID || operationID != p.PreparationOperationID {
		return nativecapture.Checkpoint{}, &IntegrityError{}
	}
	c := checkpoint.Record()
	if c.Scope.BindingID != string(p.BindingID) || c.Scope.EpochID != string(p.EpochID) || c.GenerationID != p.GenerationID || c.DatabaseIdentity != p.NativeDatabase || c.PublisherIdentity != p.NativePublisher || c.RuntimeModuleHash != p.ProgramWireHash || c.ArtifactSHA256 != p.ArtifactWasmSHA256 || c.SchemaSHA256 != p.SchemaSHA256 || c.ConfigurationSHA256 != p.ConfigSHA256 || nativeSigningRecoverySHA256(c.SigningRecovery) != p.SigningRecoverySHA256 || !slices.Equal(c.Names, p.Names) {
		return nativecapture.Checkpoint{}, &IntegrityError{}
	}
	permit, err := readEnginePermit(ctx, tx, p.BindingID)
	if err != nil {
		return nativecapture.Checkpoint{}, err
	}
	if permit.Runtime.Database != data.NativeDatabaseIdentity(c.DatabaseIdentity) || permit.Runtime.Publisher != data.NativePublisherIdentity(c.PublisherIdentity) || permit.Runtime.Module.Program != data.NativeProgramWireHash(c.RuntimeModuleHash) || permit.Runtime.Module.Wasm != data.WasmSHA256(c.ArtifactSHA256) || permit.Runtime.Module.Schema.SHA256 != c.SchemaSHA256 || !slices.Equal(permit.Runtime.Names, c.Names) {
		return nativecapture.Checkpoint{}, &IntegrityError{}
	}
	if _, err := readNativePreparedSchema(ctx, tx, checkpoint); err != nil {
		return nativecapture.Checkpoint{}, err
	}
	intent, intentRaw, err := readNativePreparationIntent(ctx, tx, p.PointID)
	if err != nil {
		return nativecapture.Checkpoint{}, err
	}
	events, err := readNativePreparationEvents(ctx, tx, intent, intentRaw)
	if err != nil || len(events) != 6 {
		return nativecapture.Checkpoint{}, &IntegrityError{}
	}
	verification, err := decodeNativePreparedVerification(p, events[4].Proof)
	if err != nil {
		return nativecapture.Checkpoint{}, err
	}
	var proof nativeCheckpointProvenance
	if _, err := strictjson.Object(provenance, "format", "point_id", "binding_id", "epoch_id", "generation_id", "checkpoint_sha256", "source_sha256", "source_evidence_sha256", "artifact_sha256", "program_wire_hash", "descriptor_sha256", "schema_sha256", "reader_disposal_sha256", "artifact_proof_sha256"); err != nil || json.Unmarshal(provenance, &proof) != nil {
		return nativecapture.Checkpoint{}, &IntegrityError{}
	}
	canonicalProof, _ := json.Marshal(proof)
	if !bytes.Equal(provenance, canonicalProof) || proof.Format != "brine-native-checkpoint-provenance-v1" || proof.PointID != p.PointID || proof.BindingID != p.BindingID || proof.EpochID != p.EpochID || proof.GenerationID != p.GenerationID || proof.CheckpointSHA256 != checkpoint.SHA256() || proof.SourceSHA256 != p.SourceSHA256 || proof.SourceEvidenceSHA256 != p.SourceWitnessSHA256 || proof.ArtifactSHA256 != c.ArtifactSHA256 || proof.ProgramWireHash != c.RuntimeModuleHash || proof.DescriptorSHA256 != nativePointHash(descriptor) || proof.SchemaSHA256 != c.SchemaSHA256 || proof.ReaderDisposalSHA256 != nativePointHash([]byte(verification.ReaderNonce+verification.ReaderViewSHA256+verification.ReaderImageID)) || proof.ArtifactProofSHA256 != "" {
		return nativecapture.Checkpoint{}, &IntegrityError{}
	}
	return checkpoint, nil
}
