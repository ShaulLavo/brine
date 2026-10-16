package store

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"math"
	"time"

	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/ops"
	"github.com/ShaulLavo/brine/internal/plan"
)

// ManagedRouteState retains deliberate route presence independently of immutable
// release history. Disk absence without this protected receipt is never proof.
type ManagedRouteState struct {
	App             string                `json:"app"`
	BindingID       data.ReplicaBindingID `json:"binding_id"`
	PlanID          string                `json:"plan_id"`
	OperationID     string                `json:"operation_id"`
	EpochID         data.ReplicaEpochID   `json:"epoch_id"`
	RuntimeSHA256   string                `json:"runtime_sha256"`
	DesiredRevision uint64                `json:"desired_revision"`
	Revision        uint64                `json:"revision"`
	Generation      uint64                `json:"generation"`
	Present         bool                  `json:"present"`
	FileHash        string                `json:"file_hash"`
	RootSHA256      string                `json:"root_sha256"`
	LoadedSHA256    string                `json:"loaded_sha256"`
}

func (s ManagedRouteState) Valid() bool {
	return validAppName(s.App) && data.ValidID(string(s.BindingID)) && data.ValidID(string(s.EpochID)) && data.ValidCatalogHash(s.RuntimeSHA256) && s.DesiredRevision > 0 && s.DesiredRevision <= math.MaxInt64 && digestPattern.MatchString(s.PlanID) && len(s.OperationID) > 0 && len(s.OperationID) <= 128 && s.Revision != 0 && s.Revision <= math.MaxInt64 && s.Generation != 0 && (s.Present && digestPattern.MatchString(s.FileHash) || !s.Present && s.FileHash == "") && data.ValidCatalogHash(s.RootSHA256) && data.ValidCatalogHash(s.LoadedSHA256)
}

func migrateManagedRoutes(ctx context.Context, tx *sql.Tx) error {
	_, err := tx.ExecContext(ctx, `CREATE TABLE data_engine_route_states(app TEXT NOT NULL,binding_id TEXT NOT NULL REFERENCES data_replica_bindings(id),revision INTEGER NOT NULL CHECK(revision>0),plan_id TEXT NOT NULL REFERENCES plans(id),operation_id TEXT NOT NULL REFERENCES operations(id),canonical BLOB NOT NULL CHECK(length(canonical)<=4096),PRIMARY KEY(app,revision));
 CREATE TABLE data_engine_route_heads(app TEXT PRIMARY KEY,revision INTEGER NOT NULL,FOREIGN KEY(app,revision) REFERENCES data_engine_route_states(app,revision));
 CREATE TRIGGER data_engine_route_states_no_update BEFORE UPDATE ON data_engine_route_states BEGIN SELECT RAISE(ABORT,'immutable route state'); END;
 CREATE TRIGGER data_engine_route_states_no_delete BEFORE DELETE ON data_engine_route_states BEGIN SELECT RAISE(ABORT,'immutable route state'); END;`)
	return err
}

func (s *Store) ReadManagedRouteState(ctx context.Context, app string) (ManagedRouteState, error) {
	return readManagedRouteState(ctx, s.db, app)
}
func readManagedRouteState(ctx context.Context, q dataQuerier, app string) (ManagedRouteState, error) {
	var state ManagedRouteState
	var raw []byte
	var binding data.ReplicaBindingID
	var revision uint64
	var plan, operation string
	err := q.QueryRowContext(ctx, `SELECT s.binding_id,s.revision,s.plan_id,s.operation_id,s.canonical FROM data_engine_route_heads h JOIN data_engine_route_states s ON s.app=h.app AND s.revision=h.revision WHERE h.app=?`, app).Scan(&binding, &revision, &plan, &operation, &raw)
	if errors.Is(err, sql.ErrNoRows) {
		return state, ErrNotFound
	}
	if err != nil {
		return state, err
	}
	if len(raw) > 4096 || json.Unmarshal(raw, &state) != nil || !state.Valid() || state.App != app || state.BindingID != binding || state.Revision != revision || state.PlanID != plan || state.OperationID != operation {
		return state, &IntegrityError{}
	}
	canonical, _ := json.Marshal(state)
	if !bytes.Equal(raw, canonical) {
		return state, &IntegrityError{}
	}
	return state, nil
}

func (s *Store) CommitManagedRouteState(ctx context.Context, expected uint64, wanted ManagedRouteState) (ManagedRouteState, error) {
	return s.commitManagedRouteState(ctx, "", expected, wanted)
}

func (s *Store) CommitManagedResolutionRouteState(ctx context.Context, resolver string, expected uint64, wanted ManagedRouteState) (ManagedRouteState, error) {
	if resolver == "" {
		return ManagedRouteState{}, ErrInvalid
	}
	return s.commitManagedRouteState(ctx, resolver, expected, wanted)
}

func (s *Store) commitManagedRouteState(ctx context.Context, resolver string, expected uint64, wanted ManagedRouteState) (ManagedRouteState, error) {
	if s.readOnly || !wanted.Valid() || wanted.Revision != expected+1 || expected >= math.MaxInt64 {
		return ManagedRouteState{}, ErrInvalid
	}
	reserved, err := s.ReadReservedEngine(ctx, wanted.BindingID)
	if err != nil {
		return ManagedRouteState{}, err
	}
	guard, err := s.guardEngineDatabase(ctx, reserved.Database.DatabaseID)
	if err != nil {
		return ManagedRouteState{}, err
	}
	defer func() { _ = guard.Release() }()
	tx, cancel, err := s.beginWrite(ctx)
	defer cancel()
	if err != nil {
		return ManagedRouteState{}, err
	}
	defer func() { _ = tx.Rollback() }()
	p, _, err := loadPlan(ctx, tx, wanted.PlanID)
	if err != nil {
		return ManagedRouteState{}, err
	}
	if p.Managed == nil || !p.Managed.Valid() || p.App != wanted.App || p.Managed.Database.BindingID != wanted.BindingID || p.Managed.Archive.EpochID != wanted.EpochID || p.Managed.RuntimeSHA256 != wanted.RuntimeSHA256 || len(p.Conflicts) != 0 {
		return ManagedRouteState{}, ErrConflict
	}
	if !managedAppOwns(ctx, tx, p.App, p.Managed.Database.IncarnationID) {
		return ManagedRouteState{}, ErrConflict
	}
	var opPlan, opApp string
	var state ops.State
	if err := tx.QueryRowContext(ctx, "SELECT COALESCE(plan_id,''),app,state FROM operations WHERE id=?", wanted.OperationID).Scan(&opPlan, &opApp, &state); err != nil {
		return ManagedRouteState{}, err
	}
	if opPlan != wanted.PlanID || opApp != wanted.App {
		return ManagedRouteState{}, ErrConflict
	}
	if resolver == "" && state != ops.Preparing && state != ops.Quiescing && state != ops.Starting && state != ops.Checking && state != ops.RollingBack {
		return ManagedRouteState{}, ErrConflict
	}
	if resolver != "" {
		if err := managedRouteResolutionOwner(ctx, tx, resolver, wanted, p); err != nil {
			return ManagedRouteState{}, err
		}
	}
	permit, err := readEnginePermit(ctx, tx, wanted.BindingID)
	actualRuntime, _ := json.Marshal(permit.Runtime)
	if err != nil || permit.Runtime.EpochID != wanted.EpochID || digest(actualRuntime) != "sha256:"+wanted.RuntimeSHA256 || wanted.Present && !permit.AllowsVerification(time.Now().UTC()) {
		return ManagedRouteState{}, ErrConflict
	}
	desired, current, err := readEffectiveManagedDesired(ctx, tx, wanted.BindingID, wanted.EpochID, wanted.RuntimeSHA256)
	if err != nil || !current || desired.Revision != wanted.DesiredRevision || desired.PlanID != wanted.PlanID || desired.OperationID != wanted.OperationID || desired.IngressServing != wanted.Present {
		return ManagedRouteState{}, ErrConflict
	}
	previous, err := readManagedRouteState(ctx, tx, wanted.App)
	if err == nil && previous == wanted {
		return wanted, tx.Commit()
	}
	if err != nil && !errors.Is(err, ErrNotFound) {
		return ManagedRouteState{}, err
	}
	if errors.Is(err, ErrNotFound) && expected != 0 || err == nil && previous.Revision != expected {
		return ManagedRouteState{}, ErrConflict
	}
	if err == nil && previous.BindingID != wanted.BindingID {
		var retired bool
		err := tx.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM data_replica_bindings b JOIN data_databases d ON d.id=b.database_id JOIN data_archive_retirements r ON r.incarnation_id=d.incarnation_id WHERE b.id=? AND r.app=? AND NOT EXISTS(SELECT 1 FROM data_active_incarnations a WHERE a.incarnation_id=d.incarnation_id))", previous.BindingID, wanted.App).Scan(&retired)
		if err != nil || !retired {
			return ManagedRouteState{}, errors.Join(ErrConflict, err)
		}
	}
	raw, err := json.Marshal(wanted)
	if err != nil {
		return ManagedRouteState{}, err
	}
	if _, err := tx.ExecContext(ctx, "INSERT INTO data_engine_route_states VALUES(?,?,?,?,?,?)", wanted.App, wanted.BindingID, wanted.Revision, wanted.PlanID, wanted.OperationID, raw); err != nil {
		return ManagedRouteState{}, err
	}
	if _, err := tx.ExecContext(ctx, "INSERT INTO data_engine_route_heads VALUES(?,?) ON CONFLICT(app) DO UPDATE SET revision=excluded.revision", wanted.App, wanted.Revision); err != nil {
		return ManagedRouteState{}, err
	}
	return wanted, tx.Commit()
}

func managedRouteResolutionOwner(ctx context.Context, tx *sql.Tx, resolver string, wanted ManagedRouteState, p plan.Plan) error {
	original, err := managedCommitOperation(ctx, tx, wanted.OperationID)
	if err != nil || original.Kind != ops.Deploy || original.State != ops.RecoveryRequired || original.PlanID != p.Hash || original.App != p.App {
		return ErrConflict
	}
	cursor, err := managedCreateCursor(ctx, tx, original.ID, p)
	if err != nil || !pendingManagedRouteReceipt(cursor) {
		return ErrConflict
	}
	current := original
	for depth := 0; depth < 64; depth++ {
		child, exists, err := managedResolutionChild(ctx, tx, current.ID)
		if err != nil || !exists || child.Kind != ops.Resolve || child.RecoveryOf != current.ID || child.PlanID != p.Hash || child.App != p.App || child.SecretRef != original.SecretRef || managedResolutionReceipt(ctx, tx, child) != nil {
			return ErrConflict
		}
		if child.ID == resolver {
			return activeManagedRouteReceiptResolver(ctx, tx, child, p)
		}
		if child.State != ops.RecoveryRequired {
			return ErrConflict
		}
		current = child
	}
	return ErrConflict
}

func activeManagedRouteReceiptResolver(ctx context.Context, tx *sql.Tx, resolver Operation, p plan.Plan) error {
	cursor, err := managedCreateCursor(ctx, tx, resolver.ID, p)
	if err != nil || resolver.State != ops.Checking || !pendingManagedRouteReceipt(cursor) {
		return ErrConflict
	}
	return nil
}

func pendingManagedRouteReceipt(cursor ops.ManagedCreateCursor) bool {
	return cursor.Boundary.Step == "managed_route_receipt" && (cursor.Boundary.Outcome == "intent" || cursor.Boundary.Outcome == "unknown") && cursor.FrontendStart.Step == ""
}
