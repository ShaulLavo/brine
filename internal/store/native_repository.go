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
	"github.com/ShaulLavo/brine/internal/strictjson"
)

type nativeRepositoryIntentRecord struct {
	Format         string                `json:"format"`
	ID             string                `json:"id"`
	OperationID    string                `json:"operation_id"`
	PlanID         string                `json:"plan_id"`
	ConsumerSHA256 string                `json:"consumer_sha256"`
	PointID        string                `json:"point_id"`
	BindingID      data.ReplicaBindingID `json:"binding_id"`
	SourceEpochID  data.ReplicaEpochID   `json:"source_epoch_id"`
	EpochID        data.ReplicaEpochID   `json:"epoch_id"`
	DatabaseID     data.DatabaseID       `json:"database_id"`
	IncarnationID  data.AppIncarnationID `json:"incarnation_id"`
	FenceID        data.FenceID          `json:"fence_id"`
	FenceOwnerOpID string                `json:"fence_owner_operation_id"`
	RuntimeVersion uint64                `json:"runtime_version"`
	RuntimeSHA256  string                `json:"runtime_sha256"`
	PolicySHA256   string                `json:"policy_sha256"`
	Archive        data.ArchiveBinding   `json:"archive"`
	ReservedAt     time.Time             `json:"reserved_at"`
}

type nativeRepositoryIntent struct {
	record              nativeRepositoryIntentRecord
	archive             data.ArchiveBinding
	raw                 []byte
	hash                string
	epochID             data.ReplicaEpochID
	creationID          string
	creationGrantSHA256 string
}

func (s *Store) reserveNativeIncomingRepositoryTx(ctx context.Context, tx *sql.Tx, guard *data.EngineLock, consumer nativePointConsumer) (nativeRepositoryIntent, error) {
	if !guard.HoldsLaunch(s.dir, consumer.binding) || consumer.kind != nativePointModulePublish && consumer.kind != nativePointLiveRestore {
		return nativeRepositoryIntent{}, ErrConflict
	}
	raw, err := consumer.canonical()
	if err != nil {
		return nativeRepositoryIntent{}, ErrInvalid
	}
	claimed, err := readNativePointConsumer(ctx, tx, consumer.pointID)
	if err != nil || !bytes.Equal(raw, claimed) {
		return nativeRepositoryIntent{}, errors.Join(ErrConflict, err)
	}
	point, err := readNativePreparedPointTx(ctx, tx, consumer.pointID)
	if err != nil || point.view.BindingID != consumer.binding || point.view.EpochID != consumer.epoch || checkNativePointCurrent(ctx, tx, point.view) != nil {
		return nativeRepositoryIntent{}, errors.Join(ErrConflict, err)
	}
	old, err := readNativeRepositoryIntentByOperation(ctx, tx, consumer.applyOperationID)
	if err == nil {
		if old.record.ConsumerSHA256 != nativePointHash(raw) || old.record.PlanID != consumer.planID || old.record.PointID != consumer.pointID {
			return nativeRepositoryIntent{}, ErrConflict
		}
		return old, nil
	}
	if !errors.Is(err, ErrNotFound) {
		return nativeRepositoryIntent{}, err
	}
	if err := checkNativeIncomingRepositoryOperation(ctx, tx, consumer); err != nil {
		return nativeRepositoryIntent{}, err
	}
	permit, err := readEnginePermit(ctx, tx, consumer.binding)
	if err != nil {
		return nativeRepositoryIntent{}, err
	}
	epoch, err := data.NewID()
	if err != nil {
		return nativeRepositoryIntent{}, err
	}
	id, err := data.NewID()
	if err != nil {
		return nativeRepositoryIntent{}, err
	}
	archive := permit.Reserved.Archive
	archive.EpochID = data.ReplicaEpochID(epoch)
	archive.RemotePrefix, err = data.RemotePrefix(archive.Destination.BasePrefix, permit.Reserved.Database.IncarnationID, permit.Reserved.Database.DatabaseID, archive.EpochID)
	if err != nil || archive.Validate(permit.Reserved.Database) != nil || checkFreshNativeRepositoryPrefix(ctx, tx, archive) != nil {
		return nativeRepositoryIntent{}, ErrConflict
	}
	v := point.view
	record := nativeRepositoryIntentRecord{Format: "brine-native-repository-intent-v1", ID: id, OperationID: consumer.applyOperationID, PlanID: consumer.planID, ConsumerSHA256: nativePointHash(raw), PointID: consumer.pointID, BindingID: consumer.binding, SourceEpochID: consumer.epoch, EpochID: archive.EpochID, DatabaseID: v.DatabaseID, IncarnationID: v.IncarnationID, FenceID: v.FenceID, FenceOwnerOpID: v.FenceOwnerOpID, RuntimeVersion: v.RuntimeVersion, RuntimeSHA256: v.RuntimeSHA256, PolicySHA256: consumer.policySHA256, Archive: archive, ReservedAt: time.Now().UTC()}
	canonical, err := json.Marshal(record)
	if err != nil || len(canonical) > 32768 {
		return nativeRepositoryIntent{}, ErrInvalid
	}
	_, err = tx.ExecContext(ctx, "INSERT INTO data_native_repository_intents VALUES(?,?,?,?,?,?,?,?)", id, record.OperationID, record.BindingID, record.EpochID, archive.Destination.Endpoint, archive.Destination.Bucket, archive.RemotePrefix, canonical)
	if err != nil {
		return nativeRepositoryIntent{}, err
	}
	return nativeRepositoryIntentValue(record, canonical), nil
}

func nativeRepositoryIntentValue(record nativeRepositoryIntentRecord, raw []byte) nativeRepositoryIntent {
	hash := nativePointHash(raw)
	return nativeRepositoryIntent{record: record, archive: record.Archive, raw: bytes.Clone(raw), hash: hash, epochID: record.EpochID, creationID: record.ID, creationGrantSHA256: hash}
}

func readNativeRepositoryIntentByOperation(ctx context.Context, q dataQuerier, operationID string) (nativeRepositoryIntent, error) {
	var id string
	err := q.QueryRowContext(ctx, "SELECT id FROM data_native_repository_intents WHERE operation_id=?", operationID).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return nativeRepositoryIntent{}, ErrNotFound
	}
	if err != nil {
		return nativeRepositoryIntent{}, err
	}
	return readNativeRepositoryIntentTx(ctx, q, id)
}

func readNativeRepositoryIntentTx(ctx context.Context, q dataQuerier, id string) (nativeRepositoryIntent, error) {
	var record nativeRepositoryIntentRecord
	var operation, endpoint, bucket, prefix string
	var binding data.ReplicaBindingID
	var epoch data.ReplicaEpochID
	var raw []byte
	err := q.QueryRowContext(ctx, "SELECT operation_id,binding_id,epoch_id,endpoint,bucket,prefix,canonical FROM data_native_repository_intents WHERE id=?", id).Scan(&operation, &binding, &epoch, &endpoint, &bucket, &prefix, &raw)
	if errors.Is(err, sql.ErrNoRows) {
		return nativeRepositoryIntent{}, ErrNotFound
	}
	if err != nil {
		return nativeRepositoryIntent{}, err
	}
	fields := []string{"format", "id", "operation_id", "plan_id", "consumer_sha256", "point_id", "binding_id", "source_epoch_id", "epoch_id", "database_id", "incarnation_id", "fence_id", "fence_owner_operation_id", "runtime_version", "runtime_sha256", "policy_sha256", "archive", "reserved_at"}
	if len(raw) == 0 || len(raw) > 32768 {
		return nativeRepositoryIntent{}, &IntegrityError{}
	}
	if _, err := strictjson.Object(raw, fields...); err != nil || json.Unmarshal(raw, &record) != nil {
		return nativeRepositoryIntent{}, &IntegrityError{}
	}
	canonical, _ := json.Marshal(record)
	if !bytes.Equal(canonical, raw) || record.Format != "brine-native-repository-intent-v1" || record.ID != id || record.OperationID != operation || record.BindingID != binding || record.EpochID != epoch || record.Archive.BindingID != binding || record.Archive.EpochID != epoch || record.Archive.Destination.Endpoint != endpoint || record.Archive.Destination.Bucket != bucket || record.Archive.RemotePrefix != prefix || !data.ValidID(record.ID) || !data.ValidID(string(record.SourceEpochID)) || record.SourceEpochID == epoch || !data.ValidCatalogHash(record.ConsumerSHA256) || !data.ValidCatalogHash(record.PolicySHA256) || !data.ValidCatalogHash(record.RuntimeSHA256) || record.ReservedAt.IsZero() || record.ReservedAt.Location() != time.UTC {
		return nativeRepositoryIntent{}, &IntegrityError{}
	}
	reserved, err := readReservedEngine(ctx, q, binding)
	if err != nil || reserved.Database.DatabaseID != record.DatabaseID || reserved.Database.IncarnationID != record.IncarnationID || record.Archive.Validate(reserved.Database) != nil {
		return nativeRepositoryIntent{}, errors.Join(&IntegrityError{}, err)
	}
	consumer, err := readNativePointConsumer(ctx, q, record.PointID)
	if err != nil || nativePointHash(consumer) != record.ConsumerSHA256 {
		return nativeRepositoryIntent{}, errors.Join(&IntegrityError{}, err)
	}
	var indexed int
	err = q.QueryRowContext(ctx, "SELECT count(*) FROM data_native_point_consumers WHERE point_id=? AND plan_id=? AND apply_operation_id=? AND binding_id=? AND epoch_id=? AND policy_sha256=?", record.PointID, record.PlanID, record.OperationID, record.BindingID, record.SourceEpochID, record.PolicySHA256).Scan(&indexed)
	if err != nil || indexed != 1 {
		return nativeRepositoryIntent{}, errors.Join(&IntegrityError{}, err)
	}
	return nativeRepositoryIntentValue(record, raw), nil
}

func checkNativeIncomingRepositoryOperation(ctx context.Context, q dataQuerier, consumer nativePointConsumer) error {
	var requester, reference, state, kind string
	err := q.QueryRowContext(ctx, "SELECT requester,secret_ref,state,kind FROM operations WHERE id=?", consumer.applyOperationID).Scan(&requester, &reference, &state, &kind)
	expected := map[nativePointConsumerKind]string{nativePointModulePublish: "module_publish_apply", nativePointLiveRestore: "native_live_restore"}
	if err != nil || requester != consumer.requester || reference != consumer.planID || kind != expected[consumer.kind] || state != "preflight" {
		return errors.Join(ErrConflict, err)
	}
	var policyHash string
	err = q.QueryRowContext(ctx, "SELECT policy_hash FROM data_admissions WHERE database_id=(SELECT database_id FROM data_replica_bindings WHERE id=?) ORDER BY version DESC LIMIT 1", consumer.binding).Scan(&policyHash)
	if err != nil || strings.TrimPrefix(policyHash, "sha256:") != consumer.policySHA256 {
		return errors.Join(ErrConflict, err)
	}
	return nil
}

func checkFreshNativeRepositoryPrefix(ctx context.Context, q dataQuerier, archive data.ArchiveBinding) error {
	if err := checkFreshNativePrefix(ctx, q, archive); err != nil {
		return err
	}
	rows, err := q.QueryContext(ctx, "SELECT prefix FROM data_native_repository_intents WHERE endpoint=? AND bucket=?", archive.Destination.Endpoint, archive.Destination.Bucket)
	if err != nil {
		return err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var prefix string
		if err := rows.Scan(&prefix); err != nil {
			return err
		}
		if nativePrefixesOverlap(prefix, archive.RemotePrefix) {
			return ErrConflict
		}
	}
	return rows.Err()
}

func checkPendingNativeRepositoryOwner(ctx context.Context, q dataQuerier, endpoint, bucket, prefix string, owner data.ReplicaBindingID) error {
	rows, err := q.QueryContext(ctx, "SELECT binding_id,prefix FROM data_native_repository_intents WHERE endpoint=? AND bucket=?", endpoint, bucket)
	if err != nil {
		return err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var binding data.ReplicaBindingID
		var recorded string
		if err := rows.Scan(&binding, &recorded); err != nil {
			return err
		}
		if binding != owner && nativePrefixesOverlap(recorded, prefix) {
			return ErrConflict
		}
	}
	return rows.Err()
}

func checkPendingNativeTransitionRepository(ctx context.Context, q dataQuerier, archive data.ArchiveBinding, input data.EngineTransition, repositoryID string) error {
	rows, err := q.QueryContext(ctx, "SELECT i.id,i.operation_id,i.binding_id,i.epoch_id,i.prefix,COALESCE(r.repository_id,'') FROM data_native_repository_intents i LEFT JOIN data_native_repository_results r ON r.intent_id=i.id WHERE i.endpoint=? AND i.bucket=?", archive.Destination.Endpoint, archive.Destination.Bucket)
	if err != nil {
		return err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var id, operation, prefix, observedRepository string
		var binding data.ReplicaBindingID
		var epoch data.ReplicaEpochID
		if err := rows.Scan(&id, &operation, &binding, &epoch, &prefix, &observedRepository); err != nil {
			return err
		}
		if !nativePrefixesOverlap(prefix, archive.RemotePrefix) {
			continue
		}
		if prefix != archive.RemotePrefix || binding != archive.BindingID || epoch != archive.EpochID || operation != input.Common().Scope.ApplyOperationID || !data.ValidID(id) || !data.ValidCatalogHash(observedRepository) || observedRepository != repositoryID {
			return ErrConflict
		}
	}
	return rows.Err()
}
