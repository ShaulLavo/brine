package archive

import (
	"encoding/json"

	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/strictjson"
)

func (d *Database) UnmarshalJSON(raw []byte) error {
	if len(raw) == 0 || len(raw) > MaxManifestBytes {
		return ErrRefused
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil || fields == nil {
		return ErrRefused
	}
	if fields["engine"] == nil {
		if exactArchiveObject(raw, []string{"binding", "epochs", "written", "definitions", "invariants", "retention"}, "allocation") != nil {
			return ErrRefused
		}
		var record SQLiteDatabase
		if json.Unmarshal(raw, &record) != nil {
			return ErrRefused
		}
		*d = SQLiteDatabaseVariant(record)
		return nil
	}
	if _, err := strictjson.Object(raw, "engine", "spacetimedb"); err != nil {
		return ErrRefused
	}
	engine, err := strictjson.Value[data.EngineKind](fields["engine"])
	if err != nil || engine != data.EngineSpacetimeDB {
		return ErrRefused
	}
	if exactArchiveObject(fields["spacetimedb"], []string{"binding", "head", "state", "allocation", "epochs", "retention", "dependencies", "transition_trees"}) != nil {
		return ErrRefused
	}
	var record NativeDatabase
	if json.Unmarshal(fields["spacetimedb"], &record) != nil {
		return ErrRefused
	}
	*d = NativeDatabaseVariant(record)
	return nil
}

func (c *Coverage) UnmarshalJSON(raw []byte) error {
	if len(raw) == 0 || len(raw) > MaxManifestBytes {
		return ErrRefused
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil || fields == nil {
		return ErrRefused
	}
	if fields["engine"] == nil {
		if exactArchiveObject(raw, []string{"database_id", "writers_quiesced_at", "observation"}, "never_written", "point", "restore_receipt") != nil {
			return ErrRefused
		}
		var record SQLiteCoverage
		if json.Unmarshal(raw, &record) != nil {
			return ErrRefused
		}
		*c = SQLiteCoverageVariant(record)
		return nil
	}
	if _, err := strictjson.Object(raw, "engine", "spacetimedb"); err != nil {
		return ErrRefused
	}
	engine, err := strictjson.Value[data.EngineKind](fields["engine"])
	if err != nil || engine != data.EngineSpacetimeDB {
		return ErrRefused
	}
	if exactArchiveObject(fields["spacetimedb"], []string{"database_id", "head", "point", "fence_id", "fence_owner_op_id", "source_witness_sha256", "checkpoint_sha256", "restore_receipt_sha256", "stop_proof_sha256", "writers_quiesced_at", "verified_at", "expires_at"}) != nil {
		return ErrRefused
	}
	var record NativeCoverage
	if json.Unmarshal(fields["spacetimedb"], &record) != nil {
		return ErrRefused
	}
	*c = NativeCoverageVariant(record)
	return nil
}

func exactArchiveObject(raw []byte, required []string, optional ...string) error {
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil || fields == nil {
		return ErrRefused
	}
	allowed := append([]string(nil), required...)
	for _, field := range optional {
		if _, exists := fields[field]; exists {
			allowed = append(allowed, field)
		}
	}
	if _, err := strictjson.Object(raw, allowed...); err != nil {
		return ErrRefused
	}
	return nil
}
