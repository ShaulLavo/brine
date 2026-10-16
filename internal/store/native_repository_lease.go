package store

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/strictjson"
)

type nativeRepositoryLeaseRecord struct {
	Format         string                `json:"format"`
	ID             string                `json:"id"`
	OperationID    string                `json:"operation_id"`
	BindingID      data.ReplicaBindingID `json:"binding_id"`
	EpochID        data.ReplicaEpochID   `json:"epoch_id"`
	IntentID       string                `json:"intent_id"`
	IntentSHA256   string                `json:"intent_sha256"`
	ConsumerSHA256 string                `json:"consumer_sha256"`
	CreatedAt      time.Time             `json:"created_at"`
}

// NativeIncomingRepository retains the real claim guard and durable exclusive
// lease. No public constructor accepts a repository scope or a creation grant.
type NativeIncomingRepository struct {
	store              *Store
	guard              *data.EngineLock
	intent             nativeRepositoryIntent
	consumer           nativePointConsumer
	lease              nativeRepositoryLeaseRecord
	leaseRaw           []byte
	resolveOperationID string
}

func (s *Store) claimNativeRepositoryCreationTx(ctx context.Context, tx *sql.Tx, guard *data.EngineLock, consumer nativePointConsumer, supplied nativeRepositoryIntent) (*NativeIncomingRepository, error) {
	if s.readOnly || !guard.HoldsLaunch(s.dir, consumer.binding) {
		return nil, ErrConflict
	}
	intent, err := readNativeRepositoryIntentTx(ctx, tx, supplied.creationID)
	if err != nil || !bytes.Equal(supplied.raw, intent.raw) || intent.record.OperationID != consumer.applyOperationID {
		return nil, errors.Join(ErrConflict, err)
	}
	raw, err := consumer.canonical()
	if err != nil || intent.record.ConsumerSHA256 != nativePointHash(raw) || checkNativeIncomingRepositoryOperation(ctx, tx, consumer) != nil {
		return nil, errors.Join(ErrConflict, err)
	}
	point, err := readNativePreparedPointTx(ctx, tx, consumer.pointID)
	if err != nil || checkNativePointCurrent(ctx, tx, point.view) != nil {
		return nil, errors.Join(ErrConflict, err)
	}
	lease, leaseRaw, state, err := readNativeRepositoryCreationLease(ctx, tx, intent)
	if err == nil {
		if state != "held" && state != "recovery_required" {
			return nil, ErrConflict
		}
		return &NativeIncomingRepository{store: s, guard: guard, intent: intent, consumer: consumer, lease: lease, leaseRaw: leaseRaw}, nil
	}
	if !errors.Is(err, ErrNotFound) {
		return nil, err
	}
	id, err := data.NewID()
	if err != nil {
		return nil, err
	}
	lease = nativeRepositoryLeaseRecord{Format: "brine-native-repository-lease-v1", ID: id, OperationID: intent.record.OperationID, BindingID: intent.record.BindingID, EpochID: intent.epochID, IntentID: intent.creationID, IntentSHA256: intent.hash, ConsumerSHA256: intent.record.ConsumerSHA256, CreatedAt: time.Now().UTC()}
	leaseRaw, err = json.Marshal(lease)
	if err != nil || len(leaseRaw) > 16384 {
		return nil, ErrInvalid
	}
	_, err = tx.ExecContext(ctx, "INSERT INTO data_native_repository_leases VALUES(?,?,?,?,'held',?)", id, lease.OperationID, lease.BindingID, lease.EpochID, leaseRaw)
	if err != nil {
		return nil, err
	}
	return &NativeIncomingRepository{store: s, guard: guard, intent: intent, consumer: consumer, lease: lease, leaseRaw: leaseRaw}, nil
}

func readNativeRepositoryCreationLease(ctx context.Context, q dataQuerier, intent nativeRepositoryIntent) (nativeRepositoryLeaseRecord, []byte, string, error) {
	var lease nativeRepositoryLeaseRecord
	var raw []byte
	var id, operation, state string
	var binding data.ReplicaBindingID
	var epoch data.ReplicaEpochID
	err := q.QueryRowContext(ctx, "SELECT id,operation_id,binding_id,epoch_id,state,canonical FROM data_native_repository_leases WHERE operation_id=? AND binding_id=? AND epoch_id=?", intent.record.OperationID, intent.record.BindingID, intent.epochID).Scan(&id, &operation, &binding, &epoch, &state, &raw)
	if errors.Is(err, sql.ErrNoRows) {
		return lease, nil, "", ErrNotFound
	}
	if err != nil {
		return lease, nil, "", err
	}
	fields := []string{"format", "id", "operation_id", "binding_id", "epoch_id", "intent_id", "intent_sha256", "consumer_sha256", "created_at"}
	if len(raw) == 0 || len(raw) > 16384 {
		return lease, nil, "", &IntegrityError{}
	}
	if _, err := strictjson.Object(raw, fields...); err != nil || json.Unmarshal(raw, &lease) != nil {
		return lease, nil, "", &IntegrityError{}
	}
	canonical, err := json.Marshal(lease)
	if err != nil || !bytes.Equal(raw, canonical) || lease.Format != "brine-native-repository-lease-v1" || !data.ValidID(lease.ID) || lease.ID != id || lease.OperationID != operation || lease.BindingID != binding || lease.EpochID != epoch || operation != intent.record.OperationID || binding != intent.record.BindingID || epoch != intent.epochID || lease.IntentID != intent.creationID || lease.IntentSHA256 != intent.hash || lease.ConsumerSHA256 != intent.record.ConsumerSHA256 || lease.CreatedAt.IsZero() || lease.CreatedAt.Location() != time.UTC || lease.CreatedAt.Before(intent.record.ReservedAt) || state != "held" && state != "released" && state != "recovery_required" {
		return lease, nil, "", &IntegrityError{}
	}
	return lease, bytes.Clone(raw), state, nil
}

func (r *NativeIncomingRepository) active(s *Store) bool {
	return r != nil && r.store == s && r.guard.HoldsLaunch(s.dir, r.intent.record.BindingID)
}

func (s *Store) readNativeIncomingRepositoryTx(ctx context.Context, tx *sql.Tx, capability *NativeIncomingRepository) (nativeRepositoryIntent, error) {
	if !capability.active(s) {
		return nativeRepositoryIntent{}, ErrConflict
	}
	intent, err := readNativeRepositoryIntentTx(ctx, tx, capability.intent.creationID)
	if err != nil || !bytes.Equal(intent.raw, capability.intent.raw) || capability.checkOperation(ctx, tx) != nil {
		return nativeRepositoryIntent{}, errors.Join(ErrConflict, err)
	}
	lease, raw, state, err := readNativeRepositoryCreationLease(ctx, tx, intent)
	if err != nil || !bytes.Equal(raw, capability.leaseRaw) || lease.ID != capability.lease.ID || state != "held" && state != "recovery_required" {
		return nativeRepositoryIntent{}, errors.Join(ErrConflict, err)
	}
	point, err := readNativePreparedPointTx(ctx, tx, capability.consumer.pointID)
	if err != nil || checkNativePointCurrent(ctx, tx, point.view) != nil || !capability.active(s) {
		return nativeRepositoryIntent{}, errors.Join(ErrConflict, err)
	}
	return intent, nil
}
