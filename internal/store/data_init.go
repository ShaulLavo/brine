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
	"github.com/ShaulLavo/brine/internal/datainit"
	"github.com/ShaulLavo/brine/internal/ops"
)

func migrateDataInitialization(ctx context.Context, tx *sql.Tx) error {
	_, err := tx.ExecContext(ctx, `
 CREATE TABLE data_init_plans(id TEXT PRIMARY KEY,canonical BLOB NOT NULL CHECK(length(canonical)<=16384));
 CREATE TRIGGER data_init_plans_no_update BEFORE UPDATE ON data_init_plans BEGIN SELECT RAISE(ABORT,'immutable initialization plan'); END;
 CREATE TRIGGER data_init_plans_no_delete BEFORE DELETE ON data_init_plans BEGIN SELECT RAISE(ABORT,'immutable initialization plan'); END;
 CREATE TABLE data_init_operations(plan_id TEXT PRIMARY KEY REFERENCES data_init_plans(id),id TEXT NOT NULL UNIQUE,fence_id TEXT NOT NULL REFERENCES data_fences(id));
 CREATE TRIGGER data_init_operations_no_update BEFORE UPDATE ON data_init_operations BEGIN SELECT RAISE(ABORT,'one initialization attempt'); END;
 CREATE TRIGGER data_init_operations_no_delete BEFORE DELETE ON data_init_operations BEGIN SELECT RAISE(ABORT,'durable initialization attempt'); END;
 CREATE TABLE data_init_events(operation_id TEXT NOT NULL REFERENCES data_init_operations(id),sequence INTEGER NOT NULL,state TEXT NOT NULL CHECK(state IN ('intent','quiesced','restore_point_intent','restore_point_verified','mutation_intent','mutation_completed','succeeded','not_initialized')),created_at TEXT NOT NULL,receipt BLOB CHECK(receipt IS NULL OR length(receipt)<=16384),PRIMARY KEY(operation_id,sequence));
 CREATE TRIGGER data_init_events_no_update BEFORE UPDATE ON data_init_events BEGIN SELECT RAISE(ABORT,'append-only initialization events'); END;
 CREATE TRIGGER data_init_events_no_delete BEFORE DELETE ON data_init_events BEGIN SELECT RAISE(ABORT,'append-only initialization events'); END;
 UPDATE schema_version SET version=8;
 `)
	return err
}

func (s *Store) SaveInitPlan(ctx context.Context, p datainit.Plan) error {
	if !p.Valid() {
		return ErrInvalid
	}
	raw, err := json.Marshal(p)
	if err != nil || len(raw) > 16384 {
		return ErrInvalid
	}
	_, err = s.db.ExecContext(ctx, "INSERT INTO data_init_plans VALUES(?,?) ON CONFLICT(id) DO NOTHING", p.ID, raw)
	return err
}
func (s *Store) LoadInitPlan(ctx context.Context, id string) (datainit.Plan, error) {
	if !datainit.ValidID(id) {
		return datainit.Plan{}, ErrInvalid
	}
	var raw []byte
	if err := s.db.QueryRowContext(ctx, "SELECT canonical FROM data_init_plans WHERE id=?", id).Scan(&raw); err != nil {
		return datainit.Plan{}, err
	}
	var p datainit.Plan
	if len(raw) > 16384 || json.Unmarshal(raw, &p) != nil || p.ID != id || !p.Valid() {
		return p, &IntegrityError{}
	}
	canonical, err := json.Marshal(p)
	if err != nil || !bytes.Equal(canonical, raw) {
		return p, &IntegrityError{}
	}
	return p, nil
}

// InitializationCandidate refuses every committed or attempted writer, and
// binds the exact desired input retained by the intended first release's plan.
func (s *Store) InitializationCandidate(ctx context.Context, app, planID string) (WriterSchema, error) {
	_, desired, err := s.LoadPlan(ctx, planID)
	if err != nil || string(desired.Name) != app || len(desired.Databases) != 1 {
		return WriterSchema{}, ErrConflict
	}
	id, err := s.ActiveDataIncarnation(ctx, app)
	if err != nil {
		return WriterSchema{}, err
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return WriterSchema{}, err
	}
	defer func() { _ = tx.Rollback() }()
	if err = untouchedInitialization(ctx, tx, app, id); err != nil {
		return WriterSchema{}, err
	}
	definitions, err := readSchemaDefinitions(ctx, tx, id)
	if err != nil {
		return WriterSchema{}, err
	}
	var database data.DatabaseID
	if err = tx.QueryRowContext(ctx, "SELECT id FROM data_databases WHERE incarnation_id=? AND name=?", id, desired.Databases[0].Name).Scan(&database); err != nil {
		return WriterSchema{}, err
	}
	permit, err := readReplicaPermit(ctx, tx, database)
	if err != nil {
		return WriterSchema{}, err
	}
	if err = s.validateReplicaPermit(ctx, tx, permit); err != nil {
		return WriterSchema{}, err
	}
	allocation, err := readAllocation(ctx, tx, database)
	if err != nil {
		return WriterSchema{}, err
	}
	allocations := map[data.DatabaseID]data.AllocationReceipt{}
	if allocation != nil {
		allocations[database] = *allocation
	}
	out := WriterSchema{Desired: desired, Bindings: []data.DatabaseBinding{permit.Database}, Definitions: definitions, Allocations: allocations}
	return out, tx.Commit()
}
func untouchedInitialization(ctx context.Context, q dataQuerier, app string, id data.AppIncarnationID) error {
	var owner string
	if err := q.QueryRowContext(ctx, "SELECT app FROM data_active_incarnations WHERE incarnation_id=?", id).Scan(&owner); err != nil {
		return err
	}
	if owner != app {
		return ErrConflict
	}
	var count int
	// Initialization, restore tests and credential tasks are not app writers; their engine owns the host lock and durable data fence.
	if err := q.QueryRowContext(ctx, `SELECT (SELECT count(*) FROM release_heads WHERE app=?)+(SELECT count(*) FROM data_writer_history h JOIN data_databases d ON d.id=h.database_id WHERE d.incarnation_id=? AND NOT EXISTS(SELECT 1 FROM data_init_operations i WHERE i.id=h.operation_id))+(SELECT count(*) FROM data_writer_starts WHERE incarnation_id=? AND cleared=0)+(SELECT count(*) FROM operations WHERE app=? AND kind NOT IN ('data_init_apply','restore_test','credential_activation') AND state NOT IN ('succeeded','failed','rolled_back','recovery_required'))`, app, id, id, app).Scan(&count); err != nil {
		return err
	}
	if count != 0 {
		return ErrConflict
	}
	return nil
}

func (s *Store) ReadInitOperation(ctx context.Context, id string) (datainit.Operation, bool, error) {
	var o datainit.Operation
	o.PlanID = id
	err := s.db.QueryRowContext(ctx, "SELECT o.id,o.fence_id,e.state FROM data_init_operations o JOIN data_init_events e ON e.operation_id=o.id WHERE o.plan_id=? ORDER BY e.sequence DESC LIMIT 1", id).Scan(&o.ID, &o.Fence, &o.State)
	if errors.Is(err, sql.ErrNoRows) {
		return o, false, nil
	}
	return o, err == nil, err
}
func (s *Store) ClaimInitialization(ctx context.Context, p datainit.Plan) (datainit.Operation, error) {
	if !p.Valid() {
		return datainit.Operation{}, ErrInvalid
	}
	tx, cancel, err := s.beginWrite(ctx)
	defer cancel()
	if err != nil {
		return datainit.Operation{}, err
	}
	defer func() { _ = tx.Rollback() }()
	if err = untouchedInitialization(ctx, tx, p.Request.App, p.Database.IncarnationID); err != nil {
		return datainit.Operation{}, err
	}
	var canonical []byte
	if err = tx.QueryRowContext(ctx, "SELECT canonical FROM data_init_plans WHERE id=?", p.ID).Scan(&canonical); err != nil {
		return datainit.Operation{}, err
	}
	raw, marshalErr := json.Marshal(p)
	if marshalErr != nil {
		return datainit.Operation{}, ErrInvalid
	}
	if !bytes.Equal(raw, canonical) {
		return datainit.Operation{}, ErrConflict
	}
	permit, err := readReplicaPermit(ctx, tx, p.Database.DatabaseID)
	if err != nil || permit.Database != p.Database || permit.Replica.EpochID != p.ReplicaEpoch || strings.TrimSuffix(permit.Replica.RemotePrefix, "/") != p.RemotePrefix || permit.FenceState == "held" {
		return datainit.Operation{}, ErrConflict
	}
	id, err := data.NewID()
	if err != nil {
		return datainit.Operation{}, err
	}
	fence, err := data.NewID()
	if err != nil {
		return datainit.Operation{}, err
	}
	o := datainit.Operation{ID: id, PlanID: p.ID, Fence: data.FenceID(fence), State: "intent"}
	if _, err = tx.ExecContext(ctx, "INSERT INTO data_fences VALUES(?,?,?,?,?)", fence, p.Database.DatabaseID, p.Database.IncarnationID, id, data.FenceHeld); err != nil {
		return o, err
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO data_init_operations VALUES(?,?,?)", p.ID, id, fence); err != nil {
		return o, err
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO data_init_events VALUES(?,1,'intent',?,NULL)", id, timestamp()); err != nil {
		return o, err
	}
	return o, tx.Commit()
}
func (s *Store) SetInitState(ctx context.Context, o datainit.Operation, state string) (datainit.Operation, error) {
	if !data.ValidID(o.ID) || !data.ValidID(string(o.Fence)) {
		return o, ErrInvalid
	}
	tx, cancel, err := s.beginWrite(ctx)
	defer cancel()
	if err != nil {
		return o, err
	}
	defer func() { _ = tx.Rollback() }()
	var current string
	var seq int
	if err = tx.QueryRowContext(ctx, "SELECT e.state,e.sequence FROM data_init_operations o JOIN data_init_events e ON e.operation_id=o.id WHERE o.id=? AND o.plan_id=? AND o.fence_id=? ORDER BY e.sequence DESC LIMIT 1", o.ID, o.PlanID, o.Fence).Scan(&current, &seq); err != nil {
		return o, err
	}
	if state == current {
		o.State = state
		return o, tx.Commit()
	}
	terminal := current == "succeeded" || current == "not_initialized"
	allowed := (state == "succeeded" || state == "not_initialized") && !terminal || current == "intent" && state == "quiesced" || current == "quiesced" && state == "restore_point_intent" || current == "restore_point_intent" && state == "restore_point_verified" || current == "restore_point_verified" && state == "mutation_intent" || current == "mutation_intent" && state == "mutation_completed"
	if state == "restore_point_verified" {
		return o, ErrConflict
	}
	if state == "succeeded" {
		var verified int
		if err = tx.QueryRowContext(ctx, "SELECT count(*) FROM data_init_events WHERE operation_id=? AND state='restore_point_verified' AND receipt IS NOT NULL", o.ID).Scan(&verified); err != nil || verified != 1 {
			return o, ErrConflict
		}
	}
	if !allowed {
		return o, ErrConflict
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO data_init_events VALUES(?,?,?,?,NULL)", o.ID, seq+1, state, timestamp()); err != nil {
		return o, err
	}
	if state == "succeeded" {
		if _, err = tx.ExecContext(ctx, "INSERT INTO data_writer_history SELECT database_id,? FROM data_fences WHERE id=? ON CONFLICT(database_id,operation_id) DO NOTHING", o.ID, o.Fence); err != nil {
			return o, err
		}
	}
	if state == "succeeded" || state == "not_initialized" {
		result, err := tx.ExecContext(ctx, "UPDATE data_fences SET state='released' WHERE id=? AND operation_id=? AND state='held'", o.Fence, o.ID)
		if err != nil {
			return o, err
		}
		count, err := result.RowsAffected()
		if err != nil || count != 1 {
			return o, ErrConflict
		}
	}
	o.State = state
	return o, tx.Commit()
}

// SaveInitRestorePoint writes the point through the shared registry. Only its
// independent verification receipt belongs to the initialization event journal.
func (s *Store) SaveInitRestorePoint(ctx context.Context, o datainit.Operation, point datainit.VerifiedRestorePoint) error {
	p, err := s.LoadInitPlan(ctx, o.PlanID)
	if err != nil || !point.Admits(p, time.Now().UTC()) || point.Receipt.OperationID != o.ID+"-empty-verify" {
		return ErrInvalid
	}
	source := point.Receipt.Source.Snapshot
	recorded := data.RestorePoint{ID: point.PointID, BindingID: p.Database.ReplicaBindingID, EpochID: p.ReplicaEpoch, Kind: data.RestorePointSnapshot, Schema: data.SchemaObservation{State: data.VerifiedEmpty, DatabaseID: p.Database.DatabaseID, ObservedAt: point.Receipt.ObservedAt, Marker: data.EmptyMarker, CatalogSHA256: data.EmptyCatalogSHA256}, Snapshot: &data.RestoreSnapshotPoint{ObjectKey: source.ObjectKey, SHA256: source.SHA256, Size: source.Size}, RecordedAt: point.Receipt.ObservedAt}
	if err = s.SaveEmptyRestorePoint(ctx, recorded, o.ID, o.Fence); err != nil {
		return err
	}
	raw, err := json.Marshal(point)
	if err != nil || len(raw) > 16384 {
		return ErrInvalid
	}
	tx, cancel, err := s.beginWrite(ctx)
	defer cancel()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	var seq int
	if err = tx.QueryRowContext(ctx, "SELECT e.sequence FROM data_init_operations o JOIN data_init_events e ON e.operation_id=o.id WHERE o.id=? AND o.plan_id=? AND o.fence_id=? AND e.sequence=(SELECT max(sequence) FROM data_init_events WHERE operation_id=o.id) AND e.state='restore_point_intent'", o.ID, o.PlanID, o.Fence).Scan(&seq); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO data_init_events VALUES(?,?,'restore_point_verified',?,?)", o.ID, seq+1, timestamp(), raw); err != nil {
		return err
	}
	return tx.Commit()
}
func (s *Store) LoadInitRestorePoint(ctx context.Context, o datainit.Operation) (datainit.VerifiedRestorePoint, error) {
	var point datainit.VerifiedRestorePoint
	var raw []byte
	if err := s.db.QueryRowContext(ctx, "SELECT receipt FROM data_init_events WHERE operation_id=? AND state='restore_point_verified' AND receipt IS NOT NULL", o.ID).Scan(&raw); err != nil {
		return point, err
	}
	if len(raw) > 16384 || json.Unmarshal(raw, &point) != nil {
		return point, &IntegrityError{}
	}
	canonical, err := json.Marshal(point)
	if err != nil || !bytes.Equal(canonical, raw) {
		return point, &IntegrityError{}
	}
	p, err := s.LoadInitPlan(ctx, o.PlanID)
	if err != nil || point.Receipt.OperationID != o.ID+"-empty-verify" || !point.Admits(p, point.Receipt.ObservedAt) {
		return point, &IntegrityError{}
	}
	registered, err := s.ReadRestorePoint(ctx, point.PointID, p.Database.ReplicaBindingID, p.ReplicaEpoch)
	snapshot := point.Receipt.Source.Snapshot
	if err != nil || registered.Kind != data.RestorePointSnapshot || registered.Snapshot == nil || registered.Schema.State != data.VerifiedEmpty || registered.Schema.DatabaseID != point.DatabaseID || !registered.RecordedAt.Equal(point.Receipt.ObservedAt) || !registered.Schema.ObservedAt.Equal(point.Receipt.ObservedAt) || registered.Snapshot.ObjectKey != snapshot.ObjectKey || registered.Snapshot.SHA256 != snapshot.SHA256 || registered.Snapshot.Size != snapshot.Size {
		return point, &IntegrityError{}
	}
	return point, nil
}

// InitializationRecoveryJobs only returns settled, abandoned tasks whose inner
// initialization fence is still held. It never enumerates active task runners.
func (s *Store) InitializationRecoveryJobs(ctx context.Context) ([]ops.Operation, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT o.id FROM operations o JOIN data_init_operations i ON i.plan_id=o.secret_ref JOIN data_fences f ON f.id=i.fence_id WHERE o.kind='data_init_apply' AND o.state IN ('recovery_required','failed') AND f.state='held' ORDER BY o.id`)
	if err != nil {
		return nil, err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			return nil, errors.Join(err, rows.Close())
		}
		ids = append(ids, id)
	}
	err = rows.Err()
	closeErr := rows.Close()
	if err != nil || closeErr != nil {
		return nil, errors.Join(err, closeErr)
	}
	out := make([]ops.Operation, 0, len(ids))
	for _, id := range ids {
		op, err := s.GetOperation(ctx, id)
		if err != nil {
			return nil, err
		}
		out = append(out, op)
	}
	return out, nil
}
