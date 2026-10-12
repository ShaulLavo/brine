package store

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/ops"
	"github.com/ShaulLavo/brine/internal/strictjson"
)

type nativePreparationIntent struct {
	PointID          string                `json:"point_id"`
	OperationID      string                `json:"operation_id"`
	FenceID          data.FenceID          `json:"fence_id"`
	BindingID        data.ReplicaBindingID `json:"binding_id"`
	EpochID          data.ReplicaEpochID   `json:"epoch_id"`
	DatabaseID       data.DatabaseID       `json:"database_id"`
	IncarnationID    data.AppIncarnationID `json:"incarnation_id"`
	Generation       uint64                `json:"generation"`
	PolicySHA256     string                `json:"policy_sha256"`
	RuntimeVersion   uint64                `json:"runtime_version"`
	RuntimeSHA256    string                `json:"runtime_sha256"`
	AllocationSHA256 string                `json:"allocation_sha256"`
	SourceKind       string                `json:"source_kind"`
	CreatedAt        time.Time             `json:"created_at"`
}

type nativePreparationEvent struct {
	PointID        string          `json:"point_id"`
	OperationID    string          `json:"operation_id"`
	Sequence       uint64          `json:"sequence"`
	Stage          string          `json:"stage"`
	PreviousSHA256 string          `json:"previous_sha256"`
	Proof          json.RawMessage `json:"proof"`
}

func readNativePreparationIntent(ctx context.Context, q dataQuerier, pointID string) (nativePreparationIntent, []byte, error) {
	var intent nativePreparationIntent
	var operation string
	var binding data.ReplicaBindingID
	var epoch data.ReplicaEpochID
	var fence data.FenceID
	var raw []byte
	err := q.QueryRowContext(ctx, "SELECT operation_id,binding_id,epoch_id,fence_id,canonical FROM data_native_preparations WHERE id=?", pointID).Scan(&operation, &binding, &epoch, &fence, &raw)
	if errors.Is(err, sql.ErrNoRows) {
		return intent, nil, ErrNotFound
	}
	if err != nil {
		return intent, nil, err
	}
	fields := []string{"point_id", "operation_id", "fence_id", "binding_id", "epoch_id", "database_id", "incarnation_id", "generation", "policy_sha256", "runtime_version", "runtime_sha256", "allocation_sha256", "source_kind", "created_at"}
	if len(raw) > 16384 {
		return intent, nil, &IntegrityError{}
	}
	if _, err := strictjson.Object(raw, fields...); err != nil || json.Unmarshal(raw, &intent) != nil {
		return intent, nil, &IntegrityError{}
	}
	canonical, _ := json.Marshal(intent)
	if !bytes.Equal(canonical, raw) || intent.PointID != pointID || intent.OperationID != operation || intent.BindingID != binding || intent.EpochID != epoch || intent.FenceID != fence || !validNativePreparationIntent(intent) {
		return intent, nil, &IntegrityError{}
	}
	return intent, bytes.Clone(raw), nil
}

func validNativePreparationIntent(i nativePreparationIntent) bool {
	return data.ValidID(i.PointID) && nativePointOperationID.MatchString(i.OperationID) && data.ValidID(string(i.FenceID)) && data.ValidID(string(i.BindingID)) && data.ValidID(string(i.EpochID)) && data.ValidID(string(i.DatabaseID)) && data.ValidID(string(i.IncarnationID)) && i.Generation <= 1<<63-1 && i.RuntimeVersion > 0 && i.RuntimeVersion <= 1<<63-1 && data.ValidCatalogHash(i.PolicySHA256) && data.ValidCatalogHash(i.RuntimeSHA256) && data.ValidCatalogHash(i.AllocationSHA256) && (i.SourceKind == "unpublished" || i.SourceKind == "initialized") && !i.CreatedAt.IsZero() && i.CreatedAt.Location() == time.UTC
}

func nativeIntentMatchesPoint(i nativePreparationIntent, v NativePreparedPointView) bool {
	return i.PointID == v.PointID && i.OperationID == v.PreparationOperationID && i.OperationID == v.FenceOwnerOpID && i.FenceID == v.FenceID && i.BindingID == v.BindingID && i.EpochID == v.EpochID && i.DatabaseID == v.DatabaseID && i.IncarnationID == v.IncarnationID && i.Generation == v.Generation && i.PolicySHA256 == v.PolicySHA256 && i.RuntimeVersion == v.RuntimeVersion && i.RuntimeSHA256 == v.RuntimeSHA256 && i.AllocationSHA256 == v.AllocationSHA256 && i.SourceKind == v.SourceKind && !v.PreparedAt.Before(i.CreatedAt)
}

func readNativePreparationEvents(ctx context.Context, q dataQuerier, intent nativePreparationIntent, intentRaw []byte) ([]nativePreparationEvent, error) {
	rows, err := q.QueryContext(ctx, "SELECT sequence,stage,previous_sha256,canonical FROM data_native_preparation_events WHERE point_id=? ORDER BY sequence", intent.PointID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	stages := []string{"stop_intent", "stopped", "source", "remote", "verified", "completed"}
	events := make([]nativePreparationEvent, 0, len(stages))
	previous := nativePointHash(intentRaw)
	for rows.Next() {
		event, raw, err := scanNativePreparationEvent(rows)
		if err != nil || len(events) == len(stages) || event.Sequence != uint64(len(events))+1 || event.Stage != stages[len(events)] || event.PointID != intent.PointID || event.OperationID != intent.OperationID || event.PreviousSHA256 != previous {
			return nil, &IntegrityError{}
		}
		events = append(events, event)
		previous = nativePointHash(raw)
	}
	return events, rows.Err()
}

func scanNativePreparationEvent(row interface{ Scan(...any) error }) (nativePreparationEvent, []byte, error) {
	var event nativePreparationEvent
	var sequence uint64
	var stage, previous string
	var raw []byte
	if err := row.Scan(&sequence, &stage, &previous, &raw); err != nil {
		return event, nil, err
	}
	if len(raw) == 0 || len(raw) > 4<<20 {
		return event, nil, &IntegrityError{}
	}
	if _, err := strictjson.Object(raw, "point_id", "operation_id", "sequence", "stage", "previous_sha256", "proof"); err != nil || json.Unmarshal(raw, &event) != nil {
		return event, nil, &IntegrityError{}
	}
	canonical, _ := json.Marshal(event)
	if !bytes.Equal(canonical, raw) || event.Sequence != sequence || event.Stage != stage || event.PreviousSHA256 != previous || len(event.Proof) == 0 || bytes.Equal(event.Proof, []byte("null")) {
		return event, nil, &IntegrityError{}
	}
	return event, raw, nil
}

func checkNativePointCompletion(ctx context.Context, tx *sql.Tx, point nativePreparedPoint) error {
	i, raw, err := readNativePreparationIntent(ctx, tx, point.view.PointID)
	if err != nil || !nativeIntentMatchesPoint(i, point.view) {
		return errors.Join(&IntegrityError{}, err)
	}
	events, err := readNativePreparationEvents(ctx, tx, i, raw)
	if err != nil || len(events) != 6 || !bytes.Equal(events[5].Proof, point.raw) || nativePointHash(events[1].Proof) != point.view.StopProofSHA256 || nativePointHash(events[4].Proof) != point.view.RestoreReceiptSHA256 {
		return errors.Join(&IntegrityError{}, err)
	}
	if err := checkNativePreparedDependencyFacts(point.view, events[2].Proof, events[3].Proof); err != nil {
		return err
	}
	var kind ops.Kind
	var state ops.State
	err = tx.QueryRowContext(ctx, "SELECT kind,state FROM operations WHERE id=?", i.OperationID).Scan(&kind, &state)
	if err != nil || kind != ops.NativePrepare || state != ops.Succeeded {
		return errors.Join(ErrConflict, err)
	}
	return nil
}

type nativePreparedSourceFacts struct {
	PointID               string                `json:"point_id"`
	BindingID             data.ReplicaBindingID `json:"binding_id"`
	EpochID               data.ReplicaEpochID   `json:"epoch_id"`
	GenerationID          string                `json:"generation_id"`
	SourceKind            string                `json:"source_kind"`
	SourceSHA256          string                `json:"source_sha256"`
	SourceSealSHA256      string                `json:"source_seal_sha256"`
	SourceWitnessSHA256   string                `json:"source_witness_sha256"`
	CheckpointSHA256      string                `json:"checkpoint_sha256"`
	ConfigSHA256          string                `json:"config_sha256"`
	SigningRecoverySHA256 string                `json:"signing_recovery_sha256"`
	NativeDatabase        string                `json:"native_database"`
	NativePublisher       string                `json:"native_publisher"`
	ProgramWireHash       string                `json:"program_wire_hash"`
	ArtifactWasmSHA256    string                `json:"artifact_wasm_sha256"`
	SchemaSHA256          string                `json:"schema_sha256"`
	Names                 []string              `json:"names"`
}

type nativePreparedRemoteFacts struct {
	PointID              string                `json:"point_id"`
	BindingID            data.ReplicaBindingID `json:"binding_id"`
	EpochID              data.ReplicaEpochID   `json:"epoch_id"`
	SourceSHA256         string                `json:"source_sha256"`
	CheckpointSHA256     string                `json:"checkpoint_sha256"`
	TransportPointSHA256 string                `json:"transport_point_sha256"`
	BundleManifestSHA256 string                `json:"bundle_manifest_sha256"`
}

func nativePreparedSourceFactBytes(v NativePreparedPointView) ([]byte, error) {
	return json.Marshal(nativePreparedSourceFacts{PointID: v.PointID, BindingID: v.BindingID, EpochID: v.EpochID, GenerationID: v.GenerationID, SourceKind: v.SourceKind, SourceSHA256: v.SourceSHA256, SourceSealSHA256: v.SourceSealSHA256, SourceWitnessSHA256: v.SourceWitnessSHA256, CheckpointSHA256: v.CheckpointSHA256, ConfigSHA256: v.ConfigSHA256, SigningRecoverySHA256: v.SigningRecoverySHA256, NativeDatabase: v.NativeDatabase, NativePublisher: v.NativePublisher, ProgramWireHash: v.ProgramWireHash, ArtifactWasmSHA256: v.ArtifactWasmSHA256, SchemaSHA256: v.SchemaSHA256, Names: v.Names})
}

func nativePreparedRemoteFactBytes(v NativePreparedPointView) ([]byte, error) {
	return json.Marshal(nativePreparedRemoteFacts{PointID: v.PointID, BindingID: v.BindingID, EpochID: v.EpochID, SourceSHA256: v.SourceSHA256, CheckpointSHA256: v.CheckpointSHA256, TransportPointSHA256: v.TransportPointSHA256, BundleManifestSHA256: v.BundleManifestSHA256})
}

func checkNativePreparedDependencyFacts(v NativePreparedPointView, sourceRaw, remoteRaw []byte) error {
	source, sourceErr := nativePreparedSourceFactBytes(v)
	remote, remoteErr := nativePreparedRemoteFactBytes(v)
	if sourceErr != nil || remoteErr != nil || !bytes.Equal(source, sourceRaw) || !bytes.Equal(remote, remoteRaw) {
		return &IntegrityError{}
	}
	return nil
}

func checkNativePointCurrent(ctx context.Context, tx *sql.Tx, v NativePreparedPointView) error {
	permit, err := readEnginePermit(ctx, tx, v.BindingID)
	if err != nil || permit.Reserved.Database.DatabaseID != v.DatabaseID || permit.Reserved.Database.IncarnationID != v.IncarnationID || permit.Runtime.EpochID != v.EpochID || string(permit.Runtime.State) != v.SourceKind || permit.Allocation == nil {
		return errors.Join(ErrConflict, err)
	}
	var raw []byte
	var version uint64
	err = tx.QueryRowContext(ctx, "SELECT version,canonical FROM data_engine_runtime_commits WHERE binding_id=? ORDER BY version DESC LIMIT 1", v.BindingID).Scan(&version, &raw)
	if err != nil || version != v.RuntimeVersion || nativePointHash(raw) != v.RuntimeSHA256 {
		return errors.Join(ErrConflict, err)
	}
	allocationRaw, _ := json.Marshal(permit.Allocation)
	if nativePointHash(allocationRaw) != v.AllocationSHA256 || v.AllocationID != string(v.DatabaseID) {
		return ErrConflict
	}
	var generation uint64
	err = tx.QueryRowContext(ctx, "SELECT (SELECT COUNT(*) FROM releases)+(SELECT COUNT(*) FROM app_removals)+(SELECT COUNT(*) FROM data_archive_retirements)").Scan(&generation)
	if err != nil || generation != v.Generation {
		return errors.Join(ErrConflict, err)
	}
	var held int
	err = tx.QueryRowContext(ctx, "SELECT count(*) FROM data_fences WHERE id=? AND database_id=? AND incarnation_id=? AND operation_id=? AND state='held'", v.FenceID, v.DatabaseID, v.IncarnationID, v.FenceOwnerOpID).Scan(&held)
	if err != nil || held != 1 {
		return errors.Join(ErrConflict, err)
	}
	return nil
}
