package store

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"github.com/ShaulLavo/brine/internal/data"
)

type EngineReservation struct {
	App        string
	PolicyHash string
	Database   data.SpacetimeDBBinding
	Archive    data.ArchiveBinding
}

type ReservedEngine struct {
	Database data.SpacetimeDBBinding `json:"database"`
	Archive  data.ArchiveBinding     `json:"archive"`
}

// ReserveEngine reserves reviewed identities only, without allocating a tree,
// publishing a module, committing a runtime, or starting an engine.
func (s *Store) ReserveEngine(ctx context.Context, request EngineReservation) (ReservedEngine, error) {
	if s.readOnly || request.App == "" || len(request.App) > 63 || !digestPattern.MatchString(request.PolicyHash) || request.Archive.Validate(request.Database) != nil {
		return ReservedEngine{}, ErrInvalid
	}
	tx, cancel, err := s.beginWrite(ctx)
	defer cancel()
	if err != nil {
		return ReservedEngine{}, err
	}
	defer func() { _ = tx.Rollback() }()
	if err = reserveEngineIncarnation(ctx, tx, request.App, request.Database.IncarnationID); err != nil {
		return ReservedEngine{}, err
	}
	existing, err := readReservedEngine(ctx, tx, request.Database.BindingID)
	if err == nil {
		wanted := ReservedEngine{Database: request.Database, Archive: request.Archive}
		before, _ := json.Marshal(existing)
		after, _ := json.Marshal(wanted)
		if !bytes.Equal(before, after) {
			return ReservedEngine{}, ErrConflict
		}
		return existing, tx.Commit()
	}
	if !errors.Is(err, ErrNotFound) {
		return ReservedEngine{}, err
	}
	if err = insertEngineReservation(ctx, tx, request); err != nil {
		return ReservedEngine{}, err
	}
	return ReservedEngine{Database: request.Database, Archive: request.Archive}, tx.Commit()
}

func reserveEngineIncarnation(ctx context.Context, tx *sql.Tx, app string, id data.AppIncarnationID) error {
	var current data.AppIncarnationID
	err := tx.QueryRowContext(ctx, "SELECT incarnation_id FROM data_active_incarnations WHERE app=?", app).Scan(&current)
	if err == nil {
		if current != id {
			return ErrConflict
		}
		return nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO data_incarnations VALUES(?,?)", id, app); err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, "INSERT INTO data_active_incarnations VALUES(?,?)", app, id)
	return err
}

func insertEngineReservation(ctx context.Context, tx *sql.Tx, r EngineReservation) error {
	d, b := r.Database, r.Archive
	if err := checkDestinationOwner(ctx, tx, b.Destination.Endpoint, b.Destination.Bucket, b.RemotePrefix, b.BindingID); err != nil {
		return err
	}
	database, err := json.Marshal(d)
	if err != nil {
		return err
	}
	archive, err := json.Marshal(b)
	if err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO data_databases VALUES(?,?,?,?,?,?)", d.DatabaseID, d.IncarnationID, d.Name, d.Root, d.RelativeDirectory, database); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO data_engine_kinds VALUES(?,'spacetimedb')", d.DatabaseID); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO data_replica_bindings VALUES(?,?,?,?,?,?,?)", b.BindingID, d.DatabaseID, b.EpochID, b.Destination.Endpoint, b.Destination.Bucket, b.RemotePrefix, archive); err != nil {
		return err
	}
	return recordDataAdmission(ctx, tx, d.DatabaseID, r.PolicyHash)
}

func (s *Store) ReadReservedEngine(ctx context.Context, binding data.ReplicaBindingID) (ReservedEngine, error) {
	return readReservedEngine(ctx, s.db, binding)
}

func readReservedEngine(ctx context.Context, q dataQuerier, binding data.ReplicaBindingID) (ReservedEngine, error) {
	var result ReservedEngine
	var engine data.EngineKind
	index := data.SpacetimeDBBinding{BindingID: binding}
	var epoch data.ReplicaEpochID
	var endpoint, bucket, prefix string
	var database, archive []byte
	err := q.QueryRowContext(ctx, "SELECT COALESCE(k.engine,''),d.canonical,b.canonical,d.id,d.incarnation_id,d.name,d.root,d.relative_directory,b.epoch_id,b.endpoint,b.bucket,b.prefix FROM data_databases d LEFT JOIN data_engine_kinds k ON k.database_id=d.id JOIN data_replica_bindings b ON b.database_id=d.id WHERE b.id=?", binding).Scan(&engine, &database, &archive, &index.DatabaseID, &index.IncarnationID, &index.Name, &index.Root, &index.RelativeDirectory, &epoch, &endpoint, &bucket, &prefix)
	if errors.Is(err, sql.ErrNoRows) {
		return result, ErrNotFound
	}
	if err != nil {
		return result, err
	}
	if engine != data.EngineSpacetimeDB || len(database) > 16<<10 || len(archive) > 16<<10 || json.Unmarshal(database, &result.Database) != nil || json.Unmarshal(archive, &result.Archive) != nil || result.Archive.Validate(result.Database) != nil || result.Database != index || result.Archive.EpochID != epoch || result.Archive.Destination.Endpoint != endpoint || result.Archive.Destination.Bucket != bucket || result.Archive.RemotePrefix != prefix {
		return result, &IntegrityError{}
	}
	dbCanonical, _ := json.Marshal(result.Database)
	archiveCanonical, _ := json.Marshal(result.Archive)
	if !bytes.Equal(database, dbCanonical) || !bytes.Equal(archive, archiveCanonical) {
		return result, &IntegrityError{}
	}
	return result, nil
}
