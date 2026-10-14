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
)

// ManagedPreparation binds an unpublished reservation to one immutable plan and
// operation. It is neither an allocation receipt nor native startup authority.
type ManagedPreparation struct {
	BindingID    data.ReplicaBindingID `json:"binding_id"`
	PlanID       string                `json:"plan_id"`
	OperationID  string                `json:"operation_id"`
	EnginePort   uint16                `json:"engine_port"`
	FrontendPort uint16                `json:"frontend_port"`
	ConfigSHA256 string                `json:"config_sha256"`
}

func (p ManagedPreparation) Valid() bool {
	return data.ValidID(string(p.BindingID)) && digestPattern.MatchString(p.PlanID) && len(p.OperationID) > 0 && len(p.OperationID) <= 128 && p.EnginePort >= 1024 && (p.FrontendPort == 0 || p.FrontendPort >= 1024 && p.FrontendPort != p.EnginePort) && data.ValidCatalogHash(p.ConfigSHA256)
}

func migrateManagedPreparations(ctx context.Context, tx *sql.Tx) error {
	_, err := tx.ExecContext(ctx, `CREATE TABLE data_engine_preparations(binding_id TEXT PRIMARY KEY REFERENCES data_replica_bindings(id),plan_id TEXT NOT NULL REFERENCES plans(id),operation_id TEXT NOT NULL REFERENCES operations(id),engine_port INTEGER NOT NULL UNIQUE CHECK(engine_port BETWEEN 1024 AND 65535),frontend_port INTEGER,canonical BLOB NOT NULL CHECK(length(canonical)<=4096));
 CREATE UNIQUE INDEX data_engine_preparations_frontend ON data_engine_preparations(frontend_port) WHERE frontend_port IS NOT NULL;
 CREATE TRIGGER data_engine_preparations_no_update BEFORE UPDATE ON data_engine_preparations BEGIN SELECT RAISE(ABORT,'immutable engine preparation'); END;
 CREATE TRIGGER data_engine_preparations_no_delete BEFORE DELETE ON data_engine_preparations BEGIN SELECT RAISE(ABORT,'immutable engine preparation'); END;`)
	return err
}

func (s *Store) ReserveManagedPreparation(ctx context.Context, operation string, planned plan.Plan, configSHA256 string) (ManagedPreparation, error) {
	m := planned.Managed
	if s.readOnly || m == nil || !m.Valid() || m.State != data.EngineUnpublished || len(planned.Conflicts) != 0 || planned.Kind != plan.Create || len(planned.Changes) != 1 || planned.Changes[0].Kind != plan.PrepareManagedEngine {
		return ManagedPreparation{}, ErrInvalid
	}
	enginePort := m.EnginePort
	if enginePort > 65535 {
		return ManagedPreparation{}, ErrInvalid
	}
	request := ManagedPreparation{BindingID: m.Database.BindingID, PlanID: planned.Hash, OperationID: operation, EnginePort: uint16(enginePort), ConfigSHA256: configSHA256}
	if m.Frontend != nil {
		port := m.Frontend.Port
		if port > 65535 {
			return ManagedPreparation{}, ErrInvalid
		}
		request.FrontendPort = uint16(port)
	}
	if !request.Valid() {
		return request, ErrInvalid
	}
	tx, cancel, err := s.beginWrite(ctx)
	defer cancel()
	if err != nil {
		return request, err
	}
	defer func() { _ = tx.Rollback() }()
	op, err := scanOperation(tx.QueryRowContext(ctx, "SELECT id,COALESCE(plan_id,''),requester,idempotency_key,state,created_at,updated_at,kind,app,secret_ref,recovery_of FROM operations WHERE id=?", operation))
	if err != nil || op.Kind != ops.Deploy || op.PlanID != planned.Hash || op.App != planned.App || op.State != ops.Preparing {
		return request, errors.Join(ErrConflict, err)
	}
	stored, desired, err := loadPlan(ctx, tx, planned.Hash)
	if err != nil {
		return request, err
	}
	approved, err := planned.CanonicalBytes()
	if err != nil {
		return request, ErrInvalid
	}
	frozen, err := stored.CanonicalBytes()
	if err != nil || !bytes.Equal(approved, frozen) {
		return request, ErrConflict
	}
	existing, err := readManagedPreparation(ctx, tx, m.Database.BindingID)
	if err == nil {
		if existing != request {
			return request, ErrConflict
		}
		return request, tx.Commit()
	}
	if !errors.Is(err, ErrNotFound) {
		return request, err
	}
	if desired.SpacetimeDB == nil || !m.AdmitsNewEngine(string(desired.SpacetimeDB.Image)) {
		return request, ErrConflict
	}
	if err = admitManagedPorts(ctx, tx, request); err != nil {
		return request, err
	}
	reservation := EngineReservation{App: planned.App, PolicyHash: planned.PolicyHash, Database: m.Database, Archive: m.Archive}
	if err = reserveEngineIncarnation(ctx, tx, reservation.App, reservation.Database.IncarnationID); err != nil {
		return request, err
	}
	reserved, err := readReservedEngine(ctx, tx, request.BindingID)
	if errors.Is(err, ErrNotFound) {
		err = insertEngineReservation(ctx, tx, reservation)
	} else if err == nil && (reserved.Database != m.Database || reserved.Archive != m.Archive) {
		err = ErrConflict
	}
	if err != nil {
		return request, err
	}
	canonical, err := json.Marshal(request)
	if err != nil {
		return request, err
	}
	var frontend any
	if request.FrontendPort != 0 {
		frontend = request.FrontendPort
	}
	_, err = tx.ExecContext(ctx, "INSERT INTO data_engine_preparations VALUES(?,?,?,?,?,?)", request.BindingID, request.PlanID, request.OperationID, request.EnginePort, frontend, canonical)
	if err != nil {
		return request, err
	}
	return request, tx.Commit()
}

func admitManagedPorts(ctx context.Context, tx *sql.Tx, request ManagedPreparation) error {
	for _, port := range []uint16{request.EnginePort, request.FrontendPort} {
		if port == 0 {
			continue
		}
		var existing int
		if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM data_engine_preparations WHERE engine_port=? OR frontend_port=?", port, port).Scan(&existing); err != nil {
			return err
		}
		if existing != 0 {
			return ErrConflict
		}
	}
	return nil
}

func (s *Store) ReadManagedPreparation(ctx context.Context, binding data.ReplicaBindingID) (ManagedPreparation, error) {
	return readManagedPreparation(ctx, s.db, binding)
}

func readManagedPreparation(ctx context.Context, q dataQuerier, binding data.ReplicaBindingID) (ManagedPreparation, error) {
	var p ManagedPreparation
	var raw []byte
	var frontend sql.NullInt64
	err := q.QueryRowContext(ctx, "SELECT plan_id,operation_id,engine_port,frontend_port,canonical FROM data_engine_preparations WHERE binding_id=?", binding).Scan(&p.PlanID, &p.OperationID, &p.EnginePort, &frontend, &raw)
	if errors.Is(err, sql.ErrNoRows) {
		return p, ErrNotFound
	}
	if err != nil {
		return p, err
	}
	p.BindingID = binding
	if frontend.Valid {
		if frontend.Int64 < 1024 || frontend.Int64 > 65535 {
			return p, &IntegrityError{}
		}
		p.FrontendPort = uint16(frontend.Int64)
	}
	var canonical ManagedPreparation
	if len(raw) > 4096 || json.Unmarshal(raw, &canonical) != nil || !canonical.Valid() {
		return p, &IntegrityError{}
	}
	p.ConfigSHA256 = canonical.ConfigSHA256
	encoded, _ := json.Marshal(p)
	if p != canonical || !bytes.Equal(raw, encoded) {
		return p, &IntegrityError{}
	}
	return p, nil
}
