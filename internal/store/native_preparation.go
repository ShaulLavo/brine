package store

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/ops"
)

// NativePreparation is minted from recorded work under the actual binding
// launch guard. Structural source records cannot construct this capability.
type NativePreparation struct {
	store       *Store
	preparation *EnginePreparation
	intent      nativePreparationIntent
	raw         []byte
}

func (p *NativePreparation) PointID() string {
	if p == nil {
		return ""
	}
	return p.intent.PointID
}

func (p *NativePreparation) active(s *Store) bool {
	return p != nil && p.store == s && p.preparation.Active(p.intent.BindingID) && p.preparation.root == s.dir
}

func (s *Store) BeginNativePreparation(ctx context.Context, preparation *EnginePreparation, operationID string) (*NativePreparation, error) {
	if s.readOnly || preparation == nil || !preparation.Active(preparation.binding) || preparation.root != s.dir {
		return nil, ErrConflict
	}
	tx, cancel, err := s.beginWrite(ctx)
	defer cancel()
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	op, err := scanOperation(tx.QueryRowContext(ctx, "SELECT id,COALESCE(plan_id,''),requester,idempotency_key,state,created_at,updated_at,kind,app,secret_ref,recovery_of FROM operations WHERE id=?", operationID))
	if err != nil || op.Kind != ops.NativePrepare || op.State != ops.Preflight {
		return nil, errors.Join(ErrConflict, err)
	}
	input, err := readNativeTaskInput(ctx, tx, op)
	if err != nil || input.BindingID != preparation.binding {
		return nil, errors.Join(ErrConflict, err)
	}
	intent, err := buildNativePreparationIntent(ctx, tx, op, input.BindingID)
	if err != nil {
		return nil, err
	}
	var existing string
	err = tx.QueryRowContext(ctx, "SELECT id FROM data_native_preparations WHERE operation_id=?", operationID).Scan(&existing)
	if err == nil {
		old, raw, err := readNativePreparationIntent(ctx, tx, existing)
		intent.PointID, intent.CreatedAt = old.PointID, old.CreatedAt
		canonical, _ := json.Marshal(intent)
		if err != nil || !bytes.Equal(raw, canonical) {
			return nil, errors.Join(ErrConflict, err)
		}
		return &NativePreparation{store: s, preparation: preparation, intent: old, raw: raw}, tx.Commit()
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	intent.PointID, err = data.NewID()
	if err != nil {
		return nil, err
	}
	raw, _ := json.Marshal(intent)
	_, err = tx.ExecContext(ctx, "INSERT INTO data_native_preparations VALUES(?,?,?,?,?,?)", intent.PointID, intent.OperationID, intent.BindingID, intent.EpochID, intent.FenceID, raw)
	if err != nil {
		return nil, err
	}
	return &NativePreparation{store: s, preparation: preparation, intent: intent, raw: raw}, tx.Commit()
}

func buildNativePreparationIntent(ctx context.Context, tx *sql.Tx, op ops.Operation, binding data.ReplicaBindingID) (nativePreparationIntent, error) {
	var intent nativePreparationIntent
	permit, err := readEnginePermit(ctx, tx, binding)
	if err != nil || permit.Allocation == nil {
		return intent, errors.Join(ErrConflict, err)
	}
	intent = nativePreparationIntent{OperationID: op.ID, BindingID: binding, EpochID: permit.Runtime.EpochID, DatabaseID: permit.Reserved.Database.DatabaseID, IncarnationID: permit.Reserved.Database.IncarnationID, SourceKind: string(permit.Runtime.State), CreatedAt: time.Now().UTC()}
	var raw []byte
	if err := tx.QueryRowContext(ctx, "SELECT version,canonical FROM data_engine_runtime_commits WHERE binding_id=? ORDER BY version DESC LIMIT 1", binding).Scan(&intent.RuntimeVersion, &raw); err != nil {
		return intent, err
	}
	intent.RuntimeSHA256 = nativePointHash(raw)
	allocation, _ := json.Marshal(permit.Allocation)
	intent.AllocationSHA256 = nativePointHash(allocation)
	if err := tx.QueryRowContext(ctx, "SELECT policy_hash FROM data_admissions WHERE database_id=? ORDER BY version DESC LIMIT 1", intent.DatabaseID).Scan(&intent.PolicySHA256); err != nil {
		return intent, err
	}
	intent.PolicySHA256 = strings.TrimPrefix(intent.PolicySHA256, "sha256:")
	var held int
	for _, fence := range permit.Fences {
		if fence.OperationID == op.ID && fence.State == data.FenceHeld {
			intent.FenceID, held = fence.ID, held+1
		}
	}
	if held != 1 || !data.ValidCatalogHash(intent.PolicySHA256) {
		return intent, ErrConflict
	}
	err = tx.QueryRowContext(ctx, "SELECT (SELECT COUNT(*) FROM releases)+(SELECT COUNT(*) FROM app_removals)+(SELECT COUNT(*) FROM data_archive_retirements)").Scan(&intent.Generation)
	return intent, err
}

// NativeStopObservation transports the host's fixed inspection facts. It has no
// authority without the actual retained launch and lifetime lock capabilities.
type NativeStopObservation struct {
	Mode              string    `json:"mode"`
	ContainerID       string    `json:"container_id"`
	ExitCode          int       `json:"exit_code"`
	OOMKilled         bool      `json:"oom_killed"`
	Forced            bool      `json:"forced"`
	FinishedAt        time.Time `json:"finished_at"`
	SourceAbsent      bool      `json:"source_absent"`
	RestartJobsAbsent bool      `json:"restart_jobs_absent"`
	IngressClosed     bool      `json:"ingress_closed"`
}

type NativeStoppedPreparation struct {
	preparation          *NativePreparation
	lifetime             *data.EngineLock
	stopSHA256           string
	completedPointSHA256 string
}

func (p *NativeStoppedPreparation) PointID() string {
	if p == nil || p.preparation == nil {
		return ""
	}
	return p.preparation.intent.PointID
}

func (p *NativeStoppedPreparation) OperationID() string {
	if p == nil || p.preparation == nil {
		return ""
	}
	return p.preparation.intent.OperationID
}

type NativeStopIntent struct {
	Mode        string    `json:"mode"`
	ContainerID string    `json:"container_id"`
	PID         uint32    `json:"pid"`
	StartTicks  uint64    `json:"start_ticks"`
	ObservedAt  time.Time `json:"observed_at"`
}

func validNativeStopIntent(intent NativeStopIntent, sourceKind string) bool {
	if intent.ObservedAt.IsZero() || intent.ObservedAt.Location() != time.UTC || intent.ObservedAt.After(time.Now().UTC()) {
		return false
	}
	if intent.Mode == "managed_clean_stop" {
		return data.ValidCatalogHash(intent.ContainerID) && intent.PID > 0 && intent.StartTicks > 0
	}
	return intent.Mode == "unpublished_absent" && sourceKind == "unpublished" && intent.ContainerID == "" && intent.PID == 0 && intent.StartTicks == 0
}

// RecordNativeStopIntent precedes any managed stop. The durable fence stays
// owned by this operation while its launch guard is released for stop-post.
func (s *Store) RecordNativeStopIntent(ctx context.Context, preparation *NativePreparation, observed NativeStopIntent) error {
	if !preparation.active(s) || !validNativeStopIntent(observed, preparation.intent.SourceKind) || observed.ObservedAt.Before(preparation.intent.CreatedAt) {
		return ErrConflict
	}
	raw, err := json.Marshal(observed)
	if err != nil {
		return ErrInvalid
	}
	tx, cancel, err := s.beginWrite(ctx)
	defer cancel()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if err := checkNativePreparationCapability(ctx, tx, preparation); err != nil {
		return err
	}
	if err := appendNativePreparationProof(ctx, tx, preparation.intent, preparation.raw, "stop_intent", raw); err != nil {
		return err
	}
	return tx.Commit()
}

func (p *NativeStoppedPreparation) active(s *Store) bool {
	return p != nil && p.preparation.active(s) && p.lifetime.HoldsLifetime(s.dir, p.preparation.intent.BindingID)
}

// ReadNativeStoppedPermit rereads protected identities under the real retained
// locks. The return is expectation data; the opaque stopped capability supplies
// the host operation's provenance and must remain active around its observations.
func (s *Store) ReadNativeStoppedPermit(ctx context.Context, capability *NativeStoppedPreparation) (EnginePermit, error) {
	if !capability.active(s) {
		return EnginePermit{}, ErrConflict
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return EnginePermit{}, err
	}
	defer func() { _ = tx.Rollback() }()
	if err := checkNativeStoppedCapability(ctx, tx, capability); err != nil {
		return EnginePermit{}, err
	}
	events, err := readNativePreparationEvents(ctx, tx, capability.preparation.intent, capability.preparation.raw)
	if err != nil || len(events) < 2 || events[1].Stage != "stopped" || nativePointHash(events[1].Proof) != capability.stopSHA256 {
		return EnginePermit{}, errors.Join(ErrConflict, err)
	}
	permit, err := readEnginePermit(ctx, tx, capability.preparation.intent.BindingID)
	if err != nil || !capability.active(s) {
		return EnginePermit{}, errors.Join(ErrConflict, err)
	}
	if permit.Runtime.State == data.EngineUnpublished {
		if err := checkNativeUntouchedHistory(ctx, tx, permit.Runtime); err != nil {
			return EnginePermit{}, err
		}
	}
	return permit, tx.Commit()
}

func checkNativeStoppedCapability(ctx context.Context, tx *sql.Tx, capability *NativeStoppedPreparation) error {
	if capability.completedPointSHA256 == "" {
		return checkNativePreparationCapability(ctx, tx, capability.preparation)
	}
	point, err := readNativePreparedPointTx(ctx, tx, capability.PointID())
	if err != nil || point.hash != capability.completedPointSHA256 || point.view.StopProofSHA256 != capability.stopSHA256 || !time.Now().UTC().Before(point.view.ExpiresAt) {
		return errors.Join(ErrConflict, err)
	}
	return checkNativePointCurrent(ctx, tx, point.view)
}

// ResumeNativePreparedPoint requires fresh retained locks and current host stop
// facts. It does not reopen preparation append authority after completion.
func (s *Store) ResumeNativePreparedPoint(ctx context.Context, preparation *EnginePreparation, lifetime *data.EngineLock, pointID string, current NativeStopObservation) (*NativeStoppedPreparation, error) {
	if s.readOnly || preparation == nil || !preparation.Active(preparation.binding) || preparation.root != s.dir || !lifetime.HoldsLifetime(s.dir, preparation.binding) {
		return nil, ErrConflict
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	point, err := readNativePreparedPointTx(ctx, tx, pointID)
	if err != nil || point.view.BindingID != preparation.binding || !validNativeStop(current, point.view.SourceKind) || current.FinishedAt.After(time.Now().UTC()) || !time.Now().UTC().Before(point.view.ExpiresAt) || checkNativePointCurrent(ctx, tx, point.view) != nil {
		return nil, errors.Join(ErrConflict, err)
	}
	intent, raw, err := readNativePreparationIntent(ctx, tx, pointID)
	if err != nil {
		return nil, err
	}
	events, err := readNativePreparationEvents(ctx, tx, intent, raw)
	if err != nil || len(events) != 6 {
		return nil, errors.Join(ErrConflict, err)
	}
	var original NativeStopObservation
	if json.Unmarshal(events[1].Proof, &original) != nil || current.Mode != original.Mode || current.ContainerID != original.ContainerID || current.Mode == "clean_exit" && !current.FinishedAt.Equal(original.FinishedAt) || current.Mode == "unpublished_absent" && current.FinishedAt.Before(original.FinishedAt) {
		return nil, ErrConflict
	}
	native := &NativePreparation{store: s, preparation: preparation, intent: intent, raw: bytes.Clone(raw)}
	stopped := &NativeStoppedPreparation{preparation: native, lifetime: lifetime, stopSHA256: point.view.StopProofSHA256, completedPointSHA256: point.hash}
	if !stopped.active(s) {
		return nil, ErrConflict
	}
	return stopped, tx.Commit()
}

// ReadNativeStoppedDescriptorPreparation reuses the actual held guard after
// revalidation. A descriptor purpose does not acquire a second binding lock.
func (s *Store) ReadNativeStoppedDescriptorPreparation(ctx context.Context, capability *NativeStoppedPreparation) (*EnginePreparation, error) {
	if _, err := s.ReadNativeStoppedPermit(ctx, capability); err != nil {
		return nil, err
	}
	if !capability.active(s) {
		return nil, ErrConflict
	}
	return capability.preparation.preparation, nil
}

func (s *Store) RecordNativeStoppedPreparation(ctx context.Context, preparation *NativePreparation, lifetime *data.EngineLock, observed NativeStopObservation) (*NativeStoppedPreparation, error) {
	if !preparation.active(s) || !lifetime.HoldsLifetime(s.dir, preparation.intent.BindingID) || !validNativeStop(observed, preparation.intent.SourceKind) {
		return nil, ErrConflict
	}
	raw, err := json.Marshal(observed)
	if err != nil {
		return nil, ErrInvalid
	}
	tx, cancel, err := s.beginWrite(ctx)
	defer cancel()
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	if err := checkNativePreparationCapability(ctx, tx, preparation); err != nil {
		return nil, err
	}
	if err := checkNativeStoppedIntent(ctx, tx, preparation, observed); err != nil {
		return nil, err
	}
	if err := appendNativePreparationProof(ctx, tx, preparation.intent, preparation.raw, "stopped", raw); err != nil {
		return nil, err
	}
	return &NativeStoppedPreparation{preparation: preparation, lifetime: lifetime, stopSHA256: nativePointHash(raw)}, tx.Commit()
}

func checkNativeStoppedIntent(ctx context.Context, tx *sql.Tx, preparation *NativePreparation, stopped NativeStopObservation) error {
	events, err := readNativePreparationEvents(ctx, tx, preparation.intent, preparation.raw)
	if err != nil || len(events) == 0 || events[0].Stage != "stop_intent" {
		return errors.Join(ErrConflict, err)
	}
	var intent NativeStopIntent
	if json.Unmarshal(events[0].Proof, &intent) != nil || !validNativeStopIntent(intent, preparation.intent.SourceKind) || intent.ContainerID != stopped.ContainerID || stopped.FinishedAt.Before(intent.ObservedAt) || stopped.FinishedAt.After(time.Now().UTC()) {
		return ErrConflict
	}
	if intent.Mode == "unpublished_absent" && stopped.Mode != "unpublished_absent" || intent.Mode == "managed_clean_stop" && stopped.Mode != "clean_exit" {
		return ErrConflict
	}
	return nil
}

func validNativeStop(observed NativeStopObservation, sourceKind string) bool {
	if observed.ExitCode != 0 || observed.OOMKilled || observed.Forced || observed.FinishedAt.IsZero() || observed.FinishedAt.Location() != time.UTC || !observed.SourceAbsent || !observed.RestartJobsAbsent || !observed.IngressClosed {
		return false
	}
	if observed.Mode == "clean_exit" {
		return data.ValidCatalogHash(observed.ContainerID)
	}
	return observed.Mode == "unpublished_absent" && sourceKind == "unpublished" && observed.ContainerID == ""
}

func checkNativePreparationCapability(ctx context.Context, tx *sql.Tx, capability *NativePreparation) error {
	i, raw, err := readNativePreparationIntent(ctx, tx, capability.intent.PointID)
	if err != nil || !bytes.Equal(raw, capability.raw) {
		return errors.Join(ErrConflict, err)
	}
	var state ops.State
	if err := tx.QueryRowContext(ctx, "SELECT state FROM operations WHERE id=?", i.OperationID).Scan(&state); err != nil || state != ops.Preflight {
		return errors.Join(ErrConflict, err)
	}
	view := NativePreparedPointView{PointID: i.PointID, PreparationOperationID: i.OperationID, FenceOwnerOpID: i.OperationID, FenceID: i.FenceID, BindingID: i.BindingID, EpochID: i.EpochID, DatabaseID: i.DatabaseID, IncarnationID: i.IncarnationID, Generation: i.Generation, PolicySHA256: i.PolicySHA256, RuntimeVersion: i.RuntimeVersion, RuntimeSHA256: i.RuntimeSHA256, AllocationID: string(i.DatabaseID), AllocationSHA256: i.AllocationSHA256, SourceKind: i.SourceKind}
	if err := checkNativePointCurrent(ctx, tx, view); err != nil {
		return err
	}
	var policyHash string
	err = tx.QueryRowContext(ctx, "SELECT policy_hash FROM data_admissions WHERE database_id=? ORDER BY version DESC LIMIT 1", i.DatabaseID).Scan(&policyHash)
	if err != nil || strings.TrimPrefix(policyHash, "sha256:") != i.PolicySHA256 {
		return errors.Join(ErrConflict, err)
	}
	return nil
}

func appendNativePreparationProof(ctx context.Context, tx *sql.Tx, intent nativePreparationIntent, intentRaw []byte, stage string, proof []byte) error {
	events, err := readNativePreparationEvents(ctx, tx, intent, intentRaw)
	if err != nil || len(proof) == 0 || len(proof) > 4<<20 || !json.Valid(proof) {
		return errors.Join(ErrInvalid, err)
	}
	stages := []string{"stop_intent", "stopped", "source", "remote", "verified", "completed"}
	for _, event := range events {
		if event.Stage != stage {
			continue
		}
		if !bytes.Equal(event.Proof, proof) {
			return ErrConflict
		}
		return nil
	}
	if len(events) == len(stages) || stage != stages[len(events)] {
		return ErrConflict
	}
	previous := nativePointHash(intentRaw)
	if len(events) != 0 {
		raw, _ := json.Marshal(events[len(events)-1])
		previous = nativePointHash(raw)
	}
	event := nativePreparationEvent{PointID: intent.PointID, OperationID: intent.OperationID, Sequence: uint64(len(events)) + 1, Stage: stage, PreviousSHA256: previous, Proof: bytes.Clone(proof)}
	raw, err := json.Marshal(event)
	if err != nil || len(raw) > 4<<20 {
		return ErrInvalid
	}
	_, err = tx.ExecContext(ctx, "INSERT INTO data_native_preparation_events(point_id,sequence,stage,previous_sha256,canonical) VALUES(?,?,?,?,?)", intent.PointID, event.Sequence, stage, previous, raw)
	return err
}
