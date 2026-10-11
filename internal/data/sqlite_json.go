package data

import (
	"encoding/json"
)

// UnmarshalJSON preserves historical SQLite bytes while refusing another
// engine's tags or fields. A store record is not an engine auto-detection input.
func (d *SQLiteDatabase) UnmarshalJSON(raw []byte) error {
	if err := engineRecord(raw, []string{"name", "persistent_root", "mount_path", "filename", "backup_destination"}, []string{"sync_interval"}); err != nil {
		return err
	}
	type record SQLiteDatabase
	var decoded record
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return err
	}
	*d = SQLiteDatabase(decoded)
	return nil
}

func (b *SQLiteDatabaseBinding) UnmarshalJSON(raw []byte) error {
	if err := engineRecord(raw, []string{"database_id", "name", "incarnation_id", "root", "relative_directory", "mount_path", "filename", "replica_binding_id"}, nil); err != nil {
		return err
	}
	type record SQLiteDatabaseBinding
	var decoded record
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return err
	}
	*b = SQLiteDatabaseBinding(decoded)
	return nil
}

func (b *ReplicaBinding) UnmarshalJSON(raw []byte) error {
	if err := engineRecord(raw, []string{"binding_id", "database_id", "destination", "epoch_id", "remote_prefix", "credential_version", "credential_file", "unit_sha256", "config_sha256", "config_content", "config_file", "socket_file", "lifetime_lock_file", "committed"}, nil); err != nil {
		return err
	}
	type record ReplicaBinding
	var decoded record
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return err
	}
	*b = ReplicaBinding(decoded)
	return nil
}

func (d *SQLiteSchemaDefinition) UnmarshalJSON(raw []byte) error {
	if err := engineRecord(raw, []string{"database", "marker", "catalog_sha256"}, nil); err != nil {
		return err
	}
	type record SQLiteSchemaDefinition
	var decoded record
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return err
	}
	*d = SQLiteSchemaDefinition(decoded)
	return nil
}

func (c *SQLiteSchemaCompatibility) UnmarshalJSON(raw []byte) error {
	if err := engineRecord(raw, []string{"database", "accepts", "startup"}, nil); err != nil {
		return err
	}
	type record SQLiteSchemaCompatibility
	var decoded record
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return err
	}
	*c = SQLiteSchemaCompatibility(decoded)
	return nil
}

func (o *SQLiteSchemaObservation) UnmarshalJSON(raw []byte) error {
	if err := engineRecord(raw, []string{"state", "database_id", "observed_at"}, []string{"marker", "catalog_sha256", "unknown_reason"}); err != nil {
		return err
	}
	type record SQLiteSchemaObservation
	var decoded record
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return err
	}
	*o = SQLiteSchemaObservation(decoded)
	return nil
}

func (p *SQLiteRestorePoint) UnmarshalJSON(raw []byte) error {
	if err := engineRecord(raw, []string{"id", "binding_id", "epoch_id", "kind", "schema", "recorded_at"}, []string{"litestream_ltx", "sqlite_snapshot"}); err != nil {
		return err
	}
	type record SQLiteRestorePoint
	var decoded record
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return err
	}
	*p = SQLiteRestorePoint(decoded)
	return nil
}
