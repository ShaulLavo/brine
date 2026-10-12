package store

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/ShaulLavo/brine/internal/data"
)

type EnginePermit struct {
	Identity   data.StartupIdentity
	Reserved   ReservedEngine
	Runtime    data.EngineRuntime
	Allocation *data.AllocationReceipt
	Fences     []data.QuiescenceFence
}

// RegisterEngineModule records expected content, not observed live authority.
// Several reviewed code versions can share one immutable schema marker.
func (s *Store) RegisterEngineModule(ctx context.Context, binding data.ReplicaBindingID, module data.EngineModuleDefinition) error {
	if s.readOnly || module.Validate() != nil {
		return ErrInvalid
	}
	reserved, err := s.ReadReservedEngine(ctx, binding)
	if err != nil {
		return err
	}
	guard, err := s.guardEngineDatabase(ctx, reserved.Database.DatabaseID)
	if err != nil {
		return err
	}
	defer func() { _ = guard.Release() }()
	tx, cancel, err := s.beginWrite(ctx)
	defer cancel()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	schema, _ := json.Marshal(module.Schema)
	if err = saveEngineRegistry(ctx, tx, engineSchemaRecord, reserved.Database.DatabaseID, module.Schema.Marker, schema); err != nil {
		return err
	}
	raw, _ := json.Marshal(module)
	if err = saveEngineRegistry(ctx, tx, engineModuleRecord, reserved.Database.DatabaseID, string(module.Program), raw); err != nil {
		return err
	}
	return tx.Commit()
}

type engineRegistryRecord uint8

const (
	engineSchemaRecord engineRegistryRecord = iota
	engineModuleRecord
)

func saveEngineRegistry(ctx context.Context, tx *sql.Tx, kind engineRegistryRecord, database data.DatabaseID, key string, raw []byte) error {
	var query, insert string
	switch kind {
	case engineSchemaRecord:
		query = "SELECT canonical FROM data_engine_schemas WHERE database_id=? AND marker=?"
		insert = "INSERT INTO data_engine_schemas VALUES(?,?,?)"
	case engineModuleRecord:
		query = "SELECT canonical FROM data_engine_modules WHERE database_id=? AND program_wire_hash=?"
		insert = "INSERT INTO data_engine_modules VALUES(?,?,?)"
	default:
		return ErrInvalid
	}
	var old []byte
	err := tx.QueryRowContext(ctx, query, database, key).Scan(&old)
	if err == nil {
		if !bytes.Equal(raw, old) {
			return ErrConflict
		}
		return nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	_, err = tx.ExecContext(ctx, insert, database, key, raw)
	return err
}

func (s *Store) CommitEngineRuntime(ctx context.Context, runtime data.EngineRuntime, fence data.FenceID, operation string) error {
	runtime = runtime.Clone()
	if s.readOnly || runtime.Validate() != nil {
		return ErrInvalid
	}
	reserved, err := s.ReadReservedEngine(ctx, runtime.BindingID)
	if err != nil || runtime.EpochID != reserved.Archive.EpochID {
		return errors.Join(ErrConflict, err)
	}
	guard, err := s.guardEngineDatabase(ctx, reserved.Database.DatabaseID)
	if err != nil {
		return err
	}
	defer func() { _ = guard.Release() }()
	tx, cancel, err := s.beginWrite(ctx)
	defer cancel()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if err = admitEngineRuntimeCommit(ctx, tx, reserved, runtime, fence, operation); err != nil {
		return err
	}
	if err = admitInitialEngineCommit(ctx, tx, reserved); err != nil {
		return err
	}
	raw, _ := json.Marshal(runtime)
	var old []byte
	err = tx.QueryRowContext(ctx, "SELECT canonical FROM data_engine_runtime_commits WHERE binding_id=? ORDER BY version DESC LIMIT 1", runtime.BindingID).Scan(&old)
	if err == nil && bytes.Equal(old, raw) {
		return tx.Commit()
	}
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	_, err = tx.ExecContext(ctx, "INSERT INTO data_engine_runtime_commits SELECT ?,COALESCE(MAX(version),0)+1,? FROM data_engine_runtime_commits WHERE binding_id=?", runtime.BindingID, raw, runtime.BindingID)
	if err != nil {
		return err
	}
	return tx.Commit()
}

func admitEngineRuntimeCommit(ctx context.Context, tx *sql.Tx, reserved ReservedEngine, runtime data.EngineRuntime, fence data.FenceID, operation string) error {
	if err := refusePendingNativeCaptureMutation(ctx, tx, reserved.Database.BindingID, operation); err != nil {
		return err
	}
	var active int
	if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM data_active_incarnations WHERE incarnation_id=?", reserved.Database.IncarnationID).Scan(&active); err != nil || active != 1 {
		return errors.Join(ErrConflict, err)
	}
	var held int
	if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM data_fences WHERE database_id=? AND id=? AND operation_id=? AND state='held'", reserved.Database.DatabaseID, fence, operation).Scan(&held); err != nil || held != 1 {
		return errors.Join(ErrConflict, err)
	}
	module, err := readEngineModule(ctx, tx, reserved.Database.DatabaseID, runtime.Module.Program)
	if err != nil || module != runtime.Module {
		return errors.Join(ErrConflict, err)
	}
	return nil
}

func readEngineModule(ctx context.Context, q dataQuerier, database data.DatabaseID, program data.NativeProgramWireHash) (data.EngineModuleDefinition, error) {
	var raw, schema []byte
	var module data.EngineModuleDefinition
	err := q.QueryRowContext(ctx, "SELECT canonical FROM data_engine_modules WHERE database_id=? AND program_wire_hash=?", database, program).Scan(&raw)
	if err != nil {
		return module, err
	}
	if len(raw) > 16<<10 || json.Unmarshal(raw, &module) != nil || module.Program != program {
		return module, &IntegrityError{}
	}
	canonical, _ := json.Marshal(module)
	if !bytes.Equal(raw, canonical) {
		return module, &IntegrityError{}
	}
	if err = q.QueryRowContext(ctx, "SELECT canonical FROM data_engine_schemas WHERE database_id=? AND marker=?", database, module.Schema.Marker).Scan(&schema); err != nil {
		return module, err
	}
	canonical, _ = json.Marshal(module.Schema)
	if !bytes.Equal(schema, canonical) {
		return module, &IntegrityError{}
	}
	return module, nil
}

func (s *Store) SaveEngineAllocation(ctx context.Context, binding data.ReplicaBindingID, receipt data.AllocationReceipt) error {
	reserved, err := s.ReadReservedEngine(ctx, binding)
	if err != nil {
		return err
	}
	if s.readOnly || !receipt.Valid() || receipt.RootProof == nil || receipt.MappingProof == nil || receipt.DatabaseID != reserved.Database.DatabaseID || receipt.IncarnationID != reserved.Database.IncarnationID || receipt.RootProof.Root != reserved.Database.Root {
		return ErrInvalid
	}
	guard, err := s.guardEngineDatabase(ctx, reserved.Database.DatabaseID)
	if err != nil {
		return err
	}
	defer func() { _ = guard.Release() }()
	raw, _ := json.Marshal(receipt)
	_, err = s.db.ExecContext(ctx, "INSERT INTO data_allocations VALUES(?,?) ON CONFLICT(database_id) DO NOTHING", receipt.DatabaseID, raw)
	if err != nil {
		return err
	}
	var stored []byte
	if err = s.db.QueryRowContext(ctx, "SELECT canonical FROM data_allocations WHERE database_id=?", receipt.DatabaseID).Scan(&stored); err != nil || !bytes.Equal(raw, stored) {
		return errors.Join(ErrConflict, err)
	}
	return nil
}

func (s *Store) ReadEnginePermit(ctx context.Context, binding data.ReplicaBindingID) (EnginePermit, error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return EnginePermit{}, err
	}
	defer func() { _ = tx.Rollback() }()
	permit, err := readEnginePermit(ctx, tx, binding)
	if err != nil {
		return EnginePermit{}, err
	}
	return permit, tx.Commit()
}

func readEnginePermit(ctx context.Context, tx *sql.Tx, binding data.ReplicaBindingID) (EnginePermit, error) {
	var p EnginePermit
	var err error
	p.Reserved, err = readReservedEngine(ctx, tx, binding)
	if err != nil {
		return p, err
	}
	head, runtime, err := readEngineHead(ctx, tx, binding)
	if err != nil {
		return p, err
	}
	p.Runtime = runtime
	p.Reserved, p.Allocation, err = projectEngineEpoch(ctx, tx, p.Reserved, head)
	if err != nil {
		return p, err
	}
	module, err := readEngineModule(ctx, tx, p.Reserved.Database.DatabaseID, p.Runtime.Module.Program)
	if err != nil || module != p.Runtime.Module {
		return p, errors.Join(&IntegrityError{}, err)
	}
	if err = readEnginePermitIdentity(ctx, tx, &p); err != nil {
		return p, err
	}
	return p, nil
}

func readEnginePermitIdentity(ctx context.Context, tx *sql.Tx, p *EnginePermit) error {
	d, b := p.Reserved.Database, p.Reserved.Archive
	var active int
	if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM data_active_incarnations WHERE incarnation_id=?", d.IncarnationID).Scan(&active); err != nil || active != 1 {
		return errors.Join(ErrConflict, err)
	}
	if err := checkDestinationOwner(ctx, tx, b.Destination.Endpoint, b.Destination.Bucket, b.RemotePrefix, b.BindingID); err != nil {
		return err
	}
	rows, err := tx.QueryContext(ctx, "SELECT id,incarnation_id,database_id,operation_id,state FROM data_fences WHERE database_id=? ORDER BY id", d.DatabaseID)
	if err != nil {
		return err
	}
	defer func() { _ = rows.Close() }()
	p.Fences = []data.QuiescenceFence{}
	fenceState := data.StartupUnfenced
	for rows.Next() {
		var fence data.QuiescenceFence
		if err = rows.Scan(&fence.ID, &fence.IncarnationID, &fence.DatabaseID, &fence.OperationID, &fence.State); err != nil {
			return err
		}
		if !data.ValidID(string(fence.ID)) || fence.IncarnationID != d.IncarnationID || fence.DatabaseID != d.DatabaseID || fence.OperationID == "" || fence.State != data.FenceHeld && fence.State != data.FenceReleased {
			return &IntegrityError{}
		}
		p.Fences = append(p.Fences, fence)
		if fence.State == data.FenceHeld {
			fenceState = data.StartupFenceHeld
		} else if fenceState != data.StartupFenceHeld {
			fenceState = data.StartupFenceReleased
		}
	}
	if err = errors.Join(rows.Err(), rows.Close()); err != nil {
		return err
	}
	if p.Allocation == nil {
		allocation, err := readInitialEngineAllocation(ctx, tx, p.Reserved)
		if err != nil {
			return err
		}
		p.Allocation = &allocation
	}
	if p.Allocation.MappingProof.Runtime != p.Runtime.Runtime {
		return &IntegrityError{}
	}
	p.Identity = data.StartupIdentity{Engine: data.EngineSpacetimeDB, IncarnationID: d.IncarnationID, DatabaseID: d.DatabaseID, BindingID: b.BindingID, EpochID: b.EpochID, Fence: fenceState, Ownership: data.DestinationLocal, SourceSettled: p.Runtime.State == data.EngineInitialized}
	return nil
}

// AllowsVerification admits stopped-source inspection only. Live launch still
// requires the opaque source-bound proof and retained startup serialization.
func (p EnginePermit) AllowsVerification(now time.Time) bool {
	return p.Identity.Allows(data.EngineSpacetimeDB) && p.Runtime.Validate() == nil && p.Runtime.SecretsAdmit(now, time.Duration(p.Runtime.StartupSeconds)*time.Second)
}
