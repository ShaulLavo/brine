package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/ShaulLavo/brine/internal/data"
)

// maxSchemaDefinitions bounds the complete incarnation registry, including the
// empty marker reserved for each database. Writers and readers share the cap.
const maxSchemaDefinitions = 16 * 128

// RegisterSchemaDefinitions is append-only within an incarnation. The caller
// holds the mutation lock; new releases cannot redefine an existing marker.
func (s *Store) RegisterSchemaDefinitions(ctx context.Context, incarnation data.AppIncarnationID, definitions []data.SchemaDefinition) error {
	if !data.ValidID(string(incarnation)) || len(definitions) > maxSchemaDefinitions {
		return ErrInvalid
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	seen := map[[2]string]bool{}
	for _, definition := range definitions {
		key := [2]string{string(definition.Database), definition.Marker}
		if !data.ValidMarker(definition.Marker) || !data.ValidCatalogHash(definition.CatalogSHA256) || seen[key] || (definition.Marker == data.EmptyMarker && definition.CatalogSHA256 != data.EmptyCatalogSHA256) {
			return ErrInvalid
		}
		seen[key] = true
		var database data.DatabaseID
		err = tx.QueryRowContext(ctx, "SELECT id FROM data_databases WHERE incarnation_id=? AND name=?", incarnation, definition.Database).Scan(&database)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		if err = registerSchema(ctx, tx, database, definition.Marker, definition.CatalogSHA256); err != nil {
			return err
		}
	}
	// Count the resulting registry inside the write transaction. Exact retries
	// insert nothing; a batch that adds too many markers rolls back in full.
	var count int
	if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM data_schema_definitions s JOIN data_databases d ON d.id=s.database_id WHERE d.incarnation_id=?", incarnation).Scan(&count); err != nil {
		return err
	}
	if count > maxSchemaDefinitions {
		return fmt.Errorf("%w: schema registry cannot exceed %d definitions", ErrInvalid, maxSchemaDefinitions)
	}
	return tx.Commit()
}
func registerSchema(ctx context.Context, tx *sql.Tx, database data.DatabaseID, marker, hash string) error {
	if _, err := tx.ExecContext(ctx, "INSERT INTO data_schema_definitions VALUES(?,?,?) ON CONFLICT(database_id,marker) DO NOTHING", database, marker, hash); err != nil {
		return err
	}
	var existing string
	if err := tx.QueryRowContext(ctx, "SELECT catalog_sha256 FROM data_schema_definitions WHERE database_id=? AND marker=?", database, marker).Scan(&existing); err != nil {
		return err
	}
	if existing != hash {
		return ErrConflict
	}
	return nil
}
func readSchemaDefinitions(ctx context.Context, q dataQuerier, incarnation data.AppIncarnationID) ([]data.SchemaDefinition, error) {
	rows, err := q.QueryContext(ctx, "SELECT d.name,s.marker,s.catalog_sha256 FROM data_schema_definitions s JOIN data_databases d ON d.id=s.database_id WHERE d.incarnation_id=? ORDER BY d.name,s.marker", incarnation)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	definitions := []data.SchemaDefinition{}
	for rows.Next() {
		var d data.SchemaDefinition
		if err = rows.Scan(&d.Database, &d.Marker, &d.CatalogSHA256); err != nil {
			return nil, err
		}
		if !data.ValidMarker(d.Marker) || !data.ValidCatalogHash(d.CatalogSHA256) || (d.Marker == data.EmptyMarker && d.CatalogSHA256 != data.EmptyCatalogSHA256) {
			return nil, &IntegrityError{}
		}
		definitions = append(definitions, d)
		if len(definitions) > maxSchemaDefinitions {
			return nil, &IntegrityError{}
		}
	}
	return definitions, rows.Err()
}
func (s *Store) ReadSchemaDefinitions(ctx context.Context, incarnation data.AppIncarnationID) ([]data.SchemaDefinition, error) {
	if !data.ValidID(string(incarnation)) {
		return nil, ErrInvalid
	}
	return readSchemaDefinitions(ctx, s.db, incarnation)
}
