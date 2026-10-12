package store

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/ops"
	"github.com/ShaulLavo/brine/internal/plan"
	"github.com/ShaulLavo/brine/internal/policy"
)

type ManagedFrontendPermit struct {
	App           string                `json:"app"`
	OperationID   string                `json:"operation_id"`
	PlanID        string                `json:"plan_id"`
	BindingID     data.ReplicaBindingID `json:"binding_id"`
	EpochID       data.ReplicaEpochID   `json:"epoch_id"`
	RuntimeSHA256 string                `json:"runtime_sha256"`
	UnitHash      string                `json:"unit_hash"`
}

type ManagedFrontendResolution struct {
	Permit  ManagedFrontendPermit
	Pending bool
	Plan    plan.Plan
	Desired policy.Desired
}

func (p ManagedFrontendPermit) Valid() bool {
	return validAppName(p.App) && len(p.OperationID) > 0 && len(p.OperationID) <= 128 && digestPattern.MatchString(p.PlanID) && data.ValidID(string(p.BindingID)) && data.ValidID(string(p.EpochID)) && data.ValidCatalogHash(p.RuntimeSHA256) && digestPattern.MatchString(p.UnitHash)
}

func migrateManagedFrontendPermits(ctx context.Context, tx *sql.Tx) error {
	_, err := tx.ExecContext(ctx, `CREATE TABLE data_engine_frontend_permits(operation_id TEXT PRIMARY KEY REFERENCES operations(id),app TEXT NOT NULL,plan_id TEXT NOT NULL REFERENCES plans(id),binding_id TEXT NOT NULL REFERENCES data_replica_bindings(id),canonical BLOB NOT NULL CHECK(length(canonical)<=4096));
 CREATE TABLE data_engine_frontend_heads(app TEXT PRIMARY KEY,operation_id TEXT NOT NULL REFERENCES data_engine_frontend_permits(operation_id));
 CREATE TRIGGER data_engine_frontend_permits_no_update BEFORE UPDATE ON data_engine_frontend_permits BEGIN SELECT RAISE(ABORT,'immutable frontend permit'); END;
 CREATE TRIGGER data_engine_frontend_permits_no_delete BEFORE DELETE ON data_engine_frontend_permits BEGIN SELECT RAISE(ABORT,'immutable frontend permit'); END;`)
	return err
}

// BindManagedFrontendPermit stores only renderer-produced unit ownership. Its
// caller remains under the host operation lock; no request supplies a unit path.
func (s *Store) BindManagedFrontendPermit(ctx context.Context, operation, planID, unitHash string) (ManagedFrontendPermit, error) {
	if s.readOnly || !digestPattern.MatchString(unitHash) {
		return ManagedFrontendPermit{}, ErrInvalid
	}
	tx, cancel, err := s.beginWrite(ctx)
	defer cancel()
	if err != nil {
		return ManagedFrontendPermit{}, err
	}
	defer func() { _ = tx.Rollback() }()
	p, _, err := loadPlan(ctx, tx, planID)
	if err != nil {
		return ManagedFrontendPermit{}, err
	}
	if p.Managed == nil || !p.Managed.Valid() || p.Managed.State != data.EngineInitialized || p.Managed.Frontend == nil || p.Kind != plan.Create && p.Kind != plan.Update || len(p.Conflicts) != 0 {
		return ManagedFrontendPermit{}, ErrConflict
	}
	var opPlan, opApp string
	var state ops.State
	if err := tx.QueryRowContext(ctx, "SELECT COALESCE(plan_id,''),app,state FROM operations WHERE id=?", operation).Scan(&opPlan, &opApp, &state); err != nil {
		return ManagedFrontendPermit{}, err
	}
	if opPlan != planID || opApp != p.App || state != ops.Starting {
		return ManagedFrontendPermit{}, ErrConflict
	}
	m := p.Managed
	permit := ManagedFrontendPermit{App: p.App, OperationID: operation, PlanID: planID, BindingID: m.Database.BindingID, EpochID: m.Archive.EpochID, RuntimeSHA256: m.RuntimeSHA256, UnitHash: unitHash}
	if !permit.Valid() {
		return permit, ErrInvalid
	}
	raw, err := json.Marshal(permit)
	if err != nil {
		return permit, err
	}
	_, err = tx.ExecContext(ctx, "INSERT INTO data_engine_frontend_permits VALUES(?,?,?,?,?) ON CONFLICT(operation_id) DO NOTHING", permit.OperationID, permit.App, permit.PlanID, permit.BindingID, raw)
	if err != nil {
		return permit, err
	}
	var stored []byte
	if err := tx.QueryRowContext(ctx, "SELECT canonical FROM data_engine_frontend_permits WHERE operation_id=?", operation).Scan(&stored); err != nil {
		return permit, err
	}
	if !bytes.Equal(raw, stored) {
		return permit, ErrConflict
	}
	_, err = tx.ExecContext(ctx, "INSERT INTO data_engine_frontend_heads VALUES(?,?) ON CONFLICT(app) DO UPDATE SET operation_id=excluded.operation_id", permit.App, operation)
	if err != nil {
		return permit, err
	}
	return permit, tx.Commit()
}

func (s *Store) ReadManagedFrontendPermit(ctx context.Context, app string) (ManagedFrontendResolution, error) {
	if !validAppName(app) {
		return ManagedFrontendResolution{}, ErrInvalid
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return ManagedFrontendResolution{}, err
	}
	defer func() { _ = tx.Rollback() }()
	resolution, err := readManagedFrontendPermit(ctx, tx, app)
	if err != nil {
		return resolution, err
	}
	return resolution, tx.Commit()
}

func readManagedFrontendPermit(ctx context.Context, tx *sql.Tx, app string) (ManagedFrontendResolution, error) {
	var resolution ManagedFrontendResolution
	var raw []byte
	var recordApp, recordPlan string
	var binding data.ReplicaBindingID
	var operation string
	err := tx.QueryRowContext(ctx, `SELECT p.operation_id,p.app,p.plan_id,p.binding_id,p.canonical FROM data_engine_frontend_heads h JOIN data_engine_frontend_permits p ON p.operation_id=h.operation_id WHERE h.app=?`, app).Scan(&operation, &recordApp, &recordPlan, &binding, &raw)
	if errors.Is(err, sql.ErrNoRows) {
		return resolution, ErrNotFound
	}
	if err != nil {
		return resolution, err
	}
	permit := &resolution.Permit
	if len(raw) > 4096 || json.Unmarshal(raw, permit) != nil || !permit.Valid() || permit.App != app || recordApp != app || permit.OperationID != operation || permit.PlanID != recordPlan || permit.BindingID != binding {
		return resolution, &IntegrityError{}
	}
	canonical, _ := json.Marshal(permit)
	if !bytes.Equal(raw, canonical) {
		return resolution, &IntegrityError{}
	}
	p, d, err := loadPlan(ctx, tx, permit.PlanID)
	if err != nil {
		return resolution, err
	}
	if p.Managed == nil || p.Managed.Frontend == nil || p.Managed.Database.BindingID != permit.BindingID || p.Managed.Archive.EpochID != permit.EpochID || p.Managed.RuntimeSHA256 != permit.RuntimeSHA256 || p.App != app || p.Kind == plan.Conflict || len(p.Conflicts) != 0 {
		return resolution, &IntegrityError{}
	}
	var opPlan, opApp string
	var state ops.State
	if err := tx.QueryRowContext(ctx, "SELECT COALESCE(plan_id,''),app,state FROM operations WHERE id=?", operation).Scan(&opPlan, &opApp, &state); err != nil {
		return resolution, err
	}
	if opPlan != permit.PlanID || opApp != app {
		return resolution, ErrConflict
	}
	resolution.Plan, resolution.Desired = p, d
	if state == ops.Starting || state == ops.Checking || state == ops.Committing {
		resolution.Pending = true
		return resolution, nil
	}
	if state != ops.Succeeded {
		return resolution, ErrConflict
	}
	var releaseID string
	if err := tx.QueryRowContext(ctx, "SELECT current_id FROM release_heads WHERE app=?", app).Scan(&releaseID); err != nil {
		return resolution, err
	}
	release, err := readRelease(ctx, tx, app, releaseID)
	if err != nil {
		return resolution, err
	}
	if release.PlanID != permit.PlanID || release.Managed == nil || release.Managed.BindingID != permit.BindingID || release.Managed.EpochID != permit.EpochID || release.Managed.RuntimeSHA256 != permit.RuntimeSHA256 || release.Managed.Frontend == nil || release.Managed.Frontend.Unit.Hash != permit.UnitHash {
		return resolution, ErrConflict
	}
	return resolution, nil
}

// RestoreManagedFrontendPermit is a conditional compensation of a known head.
// An invalid old permit is never restored over a changed module/epoch/runtime.
func (s *Store) RestoreManagedFrontendPermit(ctx context.Context, operation, previous string) error {
	if s.readOnly || operation == "" {
		return ErrInvalid
	}
	tx, cancel, err := s.beginWrite(ctx)
	defer cancel()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	var app, binding string
	if err := tx.QueryRowContext(ctx, "SELECT app,binding_id FROM data_engine_frontend_permits WHERE operation_id=?", operation).Scan(&app, &binding); err != nil {
		return err
	}
	var current string
	if err := tx.QueryRowContext(ctx, "SELECT operation_id FROM data_engine_frontend_heads WHERE app=?", app).Scan(&current); err != nil {
		return err
	}
	if current != operation {
		return ErrConflict
	}
	if previous == "" {
		_, err = tx.ExecContext(ctx, "DELETE FROM data_engine_frontend_heads WHERE app=? AND operation_id=?", app, operation)
		if err != nil {
			return err
		}
		return tx.Commit()
	}
	var oldApp, oldBinding string
	var oldRaw, newRaw []byte
	if err := tx.QueryRowContext(ctx, "SELECT app,binding_id,canonical FROM data_engine_frontend_permits WHERE operation_id=?", previous).Scan(&oldApp, &oldBinding, &oldRaw); err != nil {
		return err
	}
	if err := tx.QueryRowContext(ctx, "SELECT canonical FROM data_engine_frontend_permits WHERE operation_id=?", operation).Scan(&newRaw); err != nil {
		return err
	}
	var old, newPermit ManagedFrontendPermit
	if len(oldRaw) > 4096 || len(newRaw) > 4096 || json.Unmarshal(oldRaw, &old) != nil || json.Unmarshal(newRaw, &newPermit) != nil || !old.Valid() || !newPermit.Valid() || oldApp != app || oldBinding != binding || old.EpochID != newPermit.EpochID || old.RuntimeSHA256 != newPermit.RuntimeSHA256 {
		return ErrConflict
	}
	_, err = tx.ExecContext(ctx, "UPDATE data_engine_frontend_heads SET operation_id=? WHERE app=? AND operation_id=?", previous, app, operation)
	if err != nil {
		return err
	}
	return tx.Commit()
}
