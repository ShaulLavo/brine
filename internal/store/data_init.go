package store

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/datainit"
)

func migrateDataInitialization(ctx context.Context, tx *sql.Tx) error {
	_, err := tx.ExecContext(ctx, `
 CREATE TABLE data_init_plans(id TEXT PRIMARY KEY,canonical BLOB NOT NULL CHECK(length(canonical)<=16384));
 CREATE TRIGGER data_init_plans_no_update BEFORE UPDATE ON data_init_plans BEGIN SELECT RAISE(ABORT,'immutable initialization plan'); END;
 CREATE TRIGGER data_init_plans_no_delete BEFORE DELETE ON data_init_plans BEGIN SELECT RAISE(ABORT,'immutable initialization plan'); END;
 CREATE TABLE data_init_operations(plan_id TEXT PRIMARY KEY REFERENCES data_init_plans(id),id TEXT NOT NULL UNIQUE,fence_id TEXT NOT NULL REFERENCES data_fences(id));
 CREATE TRIGGER data_init_operations_no_update BEFORE UPDATE ON data_init_operations BEGIN SELECT RAISE(ABORT,'one initialization attempt'); END;
 CREATE TRIGGER data_init_operations_no_delete BEFORE DELETE ON data_init_operations BEGIN SELECT RAISE(ABORT,'durable initialization attempt'); END;
 CREATE TABLE data_init_events(operation_id TEXT NOT NULL REFERENCES data_init_operations(id),sequence INTEGER NOT NULL,state TEXT NOT NULL CHECK(state IN ('intent','quiesced','restore_point_intent','restore_point_verified','mutation_intent','mutation_completed','succeeded')),created_at TEXT NOT NULL,PRIMARY KEY(operation_id,sequence));
 CREATE TRIGGER data_init_events_no_update BEFORE UPDATE ON data_init_events BEGIN SELECT RAISE(ABORT,'append-only initialization events'); END;
 CREATE TRIGGER data_init_events_no_delete BEFORE DELETE ON data_init_events BEGIN SELECT RAISE(ABORT,'append-only initialization events'); END;
 CREATE TABLE data_init_restore_points(operation_id TEXT PRIMARY KEY REFERENCES data_init_operations(id),canonical BLOB NOT NULL CHECK(length(canonical)<=16384));
 CREATE TRIGGER data_init_restore_points_no_update BEFORE UPDATE ON data_init_restore_points BEGIN SELECT RAISE(ABORT,'immutable restore point'); END;
 CREATE TRIGGER data_init_restore_points_no_delete BEFORE DELETE ON data_init_restore_points BEGIN SELECT RAISE(ABORT,'immutable restore point'); END;
 UPDATE schema_version SET version=6;
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
	if err := q.QueryRowContext(ctx, `SELECT (SELECT count(*) FROM release_heads WHERE app=?)+(SELECT count(*) FROM data_writer_history h JOIN data_databases d ON d.id=h.database_id WHERE d.incarnation_id=? AND NOT EXISTS(SELECT 1 FROM data_init_operations i WHERE i.id=h.operation_id))+(SELECT count(*) FROM data_writer_starts WHERE incarnation_id=? AND cleared=0)+(SELECT count(*) FROM operations WHERE app=? AND state NOT IN ('succeeded','failed','rolled_back','recovery_required'))`, app, id, id, app).Scan(&count); err != nil {
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
	if err != nil || permit.Database != p.Database || permit.FenceState == "held" {
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
	if _, err = tx.ExecContext(ctx, "INSERT INTO data_init_events VALUES(?,1,'intent',?)", id, timestamp()); err != nil {
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
	allowed := state == "succeeded" && current != "succeeded" || current == "intent" && state == "quiesced" || current == "quiesced" && state == "restore_point_intent" || current == "restore_point_intent" && state == "restore_point_verified" || current == "restore_point_verified" && state == "mutation_intent" || current == "mutation_intent" && state == "mutation_completed"
	if !allowed {
		return o, ErrConflict
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO data_init_events VALUES(?,?,?,?)", o.ID, seq+1, state, timestamp()); err != nil {
		return o, err
	}
	if state == "succeeded" {
		if _, err = tx.ExecContext(ctx, "INSERT INTO data_writer_history SELECT database_id,? FROM data_fences WHERE id=? ON CONFLICT(database_id,operation_id) DO NOTHING", o.ID, o.Fence); err != nil {
			return o, err
		}
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

func (s *Store) SaveInitRestorePoint(ctx context.Context, o datainit.Operation, point datainit.VerifiedRestorePoint) error {
	p, err := s.LoadInitPlan(ctx, o.PlanID)
	if err != nil || !point.Admits(p, time.Now().UTC()) || point.Receipt.OperationID != o.ID+"-empty-verify" {
		return ErrInvalid
	}
	raw, err := json.Marshal(point)
	if err != nil || len(raw) > 16384 {
		return ErrInvalid
	}
	result, err := s.db.ExecContext(ctx, "INSERT INTO data_init_restore_points SELECT id,? FROM data_init_operations WHERE id=? AND plan_id=? AND fence_id=?", raw, o.ID, o.PlanID, o.Fence)
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err != nil || count != 1 {
		return ErrConflict
	}
	return nil
}
func (s *Store) LoadInitRestorePoint(ctx context.Context, o datainit.Operation) (datainit.VerifiedRestorePoint, error) {
	var point datainit.VerifiedRestorePoint
	var raw []byte
	if err := s.db.QueryRowContext(ctx, "SELECT canonical FROM data_init_restore_points WHERE operation_id=?", o.ID).Scan(&raw); err != nil {
		return point, err
	}
	if len(raw) > 16384 || json.Unmarshal(raw, &point) != nil {
		return point, &IntegrityError{}
	}
	canonical, err := json.Marshal(point)
	if err != nil || !bytes.Equal(canonical, raw) {
		return point, &IntegrityError{}
	}
	return point, nil
}
