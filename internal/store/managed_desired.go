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

// ManagedDesiredState grants independent live component intent for one exact
// protected runtime incarnation. Data transitions invalidate this scope without
// resuming writers, and private replay/cleanup does not consume live intent.
type ManagedDesiredState struct {
	BindingID       data.ReplicaBindingID `json:"binding_id"`
	EpochID         data.ReplicaEpochID   `json:"epoch_id"`
	RuntimeSHA256   string                `json:"runtime_sha256"`
	Revision        uint64                `json:"revision"`
	PlanID          string                `json:"plan_id"`
	OperationID     string                `json:"operation_id"`
	EngineRunning   bool                  `json:"engine_running"`
	FrontendRunning bool                  `json:"frontend_running"`
	IngressServing  bool                  `json:"ingress_serving"`
}

func (s ManagedDesiredState) Valid() bool {
	return data.ValidID(string(s.BindingID)) && data.ValidID(string(s.EpochID)) && (data.ValidCatalogHash(s.RuntimeSHA256) || s.RuntimeSHA256 == "" && !s.EngineRunning && !s.FrontendRunning && !s.IngressServing) && s.Revision != 0 && s.Revision <= math.MaxInt64 && digestPattern.MatchString(s.PlanID) && len(s.OperationID) > 0 && len(s.OperationID) <= 128 && (!s.FrontendRunning && !s.IngressServing || s.EngineRunning)
}
func (s ManagedDesiredState) Current(binding data.ReplicaBindingID, epoch data.ReplicaEpochID, runtimeSHA256 string) bool {
	return s.Valid() && s.BindingID == binding && s.EpochID == epoch && s.RuntimeSHA256 == runtimeSHA256
}

func migrateManagedDesired(ctx context.Context, tx *sql.Tx) error {
	_, err := tx.ExecContext(ctx, `CREATE TABLE data_engine_desired_states(binding_id TEXT NOT NULL REFERENCES data_replica_bindings(id),revision INTEGER NOT NULL CHECK(revision>0),plan_id TEXT NOT NULL REFERENCES plans(id),operation_id TEXT NOT NULL REFERENCES operations(id),canonical BLOB NOT NULL CHECK(length(canonical)<=4096),PRIMARY KEY(binding_id,revision));
 CREATE TABLE data_engine_desired_heads(binding_id TEXT PRIMARY KEY REFERENCES data_replica_bindings(id),revision INTEGER NOT NULL,FOREIGN KEY(binding_id,revision) REFERENCES data_engine_desired_states(binding_id,revision));
 CREATE TRIGGER data_engine_desired_states_no_update BEFORE UPDATE ON data_engine_desired_states BEGIN SELECT RAISE(ABORT,'immutable desired state'); END;
 CREATE TRIGGER data_engine_desired_states_no_delete BEFORE DELETE ON data_engine_desired_states BEGIN SELECT RAISE(ABORT,'immutable desired state'); END;`)
	return err
}

func (s *Store) ReadManagedDesired(ctx context.Context, binding data.ReplicaBindingID) (ManagedDesiredState, error) {
	return readManagedDesired(ctx, s.db, binding)
}

func readManagedDesired(ctx context.Context, q dataQuerier, binding data.ReplicaBindingID) (ManagedDesiredState, error) {
	var state ManagedDesiredState
	var raw []byte
	var revision uint64
	var plan, operation string
	err := q.QueryRowContext(ctx, `SELECT s.revision,s.plan_id,s.operation_id,s.canonical FROM data_engine_desired_heads h JOIN data_engine_desired_states s ON s.binding_id=h.binding_id AND s.revision=h.revision WHERE h.binding_id=?`, binding).Scan(&revision, &plan, &operation, &raw)
	if errors.Is(err, sql.ErrNoRows) {
		return state, ErrNotFound
	}
	if err != nil {
		return state, err
	}
	if len(raw) > 4096 || json.Unmarshal(raw, &state) != nil || !state.Valid() || state.BindingID != binding || state.Revision != revision || state.PlanID != plan || state.OperationID != operation {
		return state, &IntegrityError{}
	}
	canonical, _ := json.Marshal(state)
	if !bytes.Equal(raw, canonical) {
		return state, &IntegrityError{}
	}
	return state, nil
}

// CommitManagedDesired CAS-selects live intent from an immutable managed plan.
// The launch guard excludes simultaneous boot/transition authority handoffs;
// callers release it before waiting for systemd start or stop.
func (s *Store) CommitManagedDesired(ctx context.Context, operation string, expected uint64, wanted ManagedDesiredState) (ManagedDesiredState, error) {
	if s.readOnly || !wanted.Valid() || wanted.OperationID != operation || wanted.Revision != expected+1 || expected == ^uint64(0) {
		return ManagedDesiredState{}, ErrInvalid
	}
	reserved, err := s.ReadReservedEngine(ctx, wanted.BindingID)
	if err != nil {
		return ManagedDesiredState{}, err
	}
	guard, err := s.guardEngineDatabase(ctx, reserved.Database.DatabaseID)
	if err != nil {
		return ManagedDesiredState{}, err
	}
	defer func() { _ = guard.Release() }()
	tx, cancel, err := s.beginWrite(ctx)
	defer cancel()
	if err != nil {
		return ManagedDesiredState{}, err
	}
	defer func() { _ = tx.Rollback() }()
	p, _, err := loadPlan(ctx, tx, wanted.PlanID)
	if err != nil {
		return ManagedDesiredState{}, err
	}
	if p.Managed == nil || !p.Managed.Valid() || len(p.Conflicts) != 0 || p.Managed.Database.BindingID != wanted.BindingID || p.Managed.Archive.EpochID != wanted.EpochID || p.Managed.RuntimeSHA256 != wanted.RuntimeSHA256 {
		return ManagedDesiredState{}, ErrConflict
	}
	if !managedDesiredAllowed(p, wanted) {
		return ManagedDesiredState{}, ErrConflict
	}
	if wanted.FrontendRunning && p.Managed.Frontend == nil {
		return ManagedDesiredState{}, ErrConflict
	}
	var operationPlan, app string
	var state ops.State
	if err := tx.QueryRowContext(ctx, "SELECT COALESCE(plan_id,''),app,state FROM operations WHERE id=?", operation).Scan(&operationPlan, &app, &state); err != nil {
		return ManagedDesiredState{}, err
	}
	if operationPlan != wanted.PlanID || app != p.App || state != ops.Preparing && state != ops.Quiescing && state != ops.Starting {
		return ManagedDesiredState{}, ErrConflict
	}
	previous, err := readManagedDesired(ctx, tx, wanted.BindingID)
	if err == nil && previous == wanted {
		return wanted, tx.Commit()
	}
	if err != nil && !errors.Is(err, ErrNotFound) {
		return ManagedDesiredState{}, err
	}
	if errors.Is(err, ErrNotFound) && expected != 0 || err == nil && previous.Revision != expected {
		return ManagedDesiredState{}, ErrConflict
	}
	if expected != p.ManagedIntentRevision && (err != nil || previous.OperationID != operation || previous.PlanID != wanted.PlanID || expected != p.ManagedIntentRevision+1 || p.Lifecycle != plan.RestartApp) {
		return ManagedDesiredState{}, ErrConflict
	}
	if wanted.RuntimeSHA256 != "" {
		runtime, err := readEnginePermit(ctx, tx, wanted.BindingID)
		if err != nil {
			return ManagedDesiredState{}, err
		}
		rawRuntime, _ := json.Marshal(runtime.Runtime)
		if digest(rawRuntime) != "sha256:"+wanted.RuntimeSHA256 || runtime.Runtime.EpochID != wanted.EpochID || wanted.EngineRunning && !runtime.AllowsVerification(time.Now().UTC()) {
			return ManagedDesiredState{}, ErrConflict
		}
	}
	canonical, err := json.Marshal(wanted)
	if err != nil {
		return ManagedDesiredState{}, err
	}
	if _, err := tx.ExecContext(ctx, "INSERT INTO data_engine_desired_states VALUES(?,?,?,?,?)", wanted.BindingID, wanted.Revision, wanted.PlanID, wanted.OperationID, canonical); err != nil {
		return ManagedDesiredState{}, err
	}
	_, err = tx.ExecContext(ctx, "INSERT INTO data_engine_desired_heads VALUES(?,?) ON CONFLICT(binding_id) DO UPDATE SET revision=excluded.revision", wanted.BindingID, wanted.Revision)
	if err != nil {
		return ManagedDesiredState{}, err
	}
	return wanted, tx.Commit()
}

// readEffectiveManagedDesired is a same-transaction freshness check usable by
// native epoch transitions. A historical running row is not a grant for the
// incoming epoch/runtime; absence and scope mismatch have zero live authority.
func readEffectiveManagedDesired(ctx context.Context, q dataQuerier, binding data.ReplicaBindingID, epoch data.ReplicaEpochID, runtimeSHA256 string) (ManagedDesiredState, bool, error) {
	state, err := readManagedDesired(ctx, q, binding)
	if errors.Is(err, ErrNotFound) {
		return ManagedDesiredState{}, false, nil
	}
	if err != nil {
		return ManagedDesiredState{}, false, err
	}
	return state, state.Current(binding, epoch, runtimeSHA256), nil
}

func managedDesiredAllowed(p plan.Plan, wanted ManagedDesiredState) bool {
	if p.Managed == nil {
		return false
	}
	flags := plan.DesiredComponentsForDeployment(*p.Managed)
	if p.Managed.State == data.EngineUnpublished {
		flags = plan.ManagedLifecyclePlan{}
	}
	if p.ManagedLifecycle != nil {
		flags = *p.ManagedLifecycle
	}
	matches := func(engine, frontend, ingress bool) bool {
		return wanted.EngineRunning == engine && wanted.FrontendRunning == frontend && wanted.IngressServing == ingress
	}
	if matches(flags.EngineRunning, flags.FrontendRunning, flags.IngressServing) {
		return true
	}
	return p.Lifecycle == plan.RestartApp && matches(!flags.Engine && flags.EngineRunning, false, false)
}
