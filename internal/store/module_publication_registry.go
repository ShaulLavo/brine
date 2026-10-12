package store

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/datapublish"
)

// Check the actual extracted candidate against immutable registered names
// before approval makes publication executable. Absence is a new definition;
// unreadable or conflicting registry state cannot become a later apply failure.
func checkPublicationCandidateRegistry(ctx context.Context, tx *sql.Tx, database data.DatabaseID, marker string, candidate datapublish.Candidate) error {
	schema := data.EngineSchemaDefinition{Marker: marker, Canonicalizer: candidate.Schema().Version(), SHA256: candidate.Schema().SHA256()}
	if schema.Validate() != nil {
		return ErrConflict
	}
	expected, _ := json.Marshal(schema)
	var registered []byte
	schemaErr := tx.QueryRowContext(ctx, "SELECT canonical FROM data_engine_schemas WHERE database_id=? AND marker=?", database, marker).Scan(&registered)
	if schemaErr != nil && !errors.Is(schemaErr, sql.ErrNoRows) {
		return schemaErr
	}
	if schemaErr == nil && !bytes.Equal(registered, expected) {
		return ErrConflict
	}
	var module []byte
	err := tx.QueryRowContext(ctx, "SELECT canonical FROM data_engine_modules WHERE database_id=? AND program_wire_hash=?", database, candidate.ProgramWireHash()).Scan(&module)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	wanted := data.EngineModuleDefinition{Program: data.NativeProgramWireHash(candidate.ProgramWireHash()), Wasm: data.WasmSHA256(candidate.WasmSHA256()), Schema: schema}
	raw, _ := json.Marshal(wanted)
	if schemaErr != nil {
		return &IntegrityError{}
	}
	if !bytes.Equal(module, raw) {
		return ErrConflict
	}
	return nil
}
