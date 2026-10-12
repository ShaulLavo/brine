package store

import (
	"context"
	"database/sql"
)

func migrateNativeEngineTransitions(ctx context.Context, tx *sql.Tx) error {
	_, err := tx.ExecContext(ctx, `
CREATE TABLE data_engine_transition_inputs(
 id TEXT PRIMARY KEY,
 apply_operation_id TEXT NOT NULL UNIQUE REFERENCES operations(id),
 prepared_point_id TEXT NOT NULL UNIQUE,
 binding_id TEXT NOT NULL REFERENCES data_replica_bindings(id),
 canonical BLOB NOT NULL CHECK(length(canonical)<=65536)
);
CREATE TABLE data_native_epochs(
 epoch_id TEXT PRIMARY KEY,
 binding_id TEXT NOT NULL REFERENCES data_replica_bindings(id),
 predecessor_epoch_id TEXT NOT NULL,
 transition_id TEXT NOT NULL UNIQUE REFERENCES data_engine_transition_inputs(id),
 endpoint TEXT NOT NULL,
 bucket TEXT NOT NULL,
 prefix TEXT NOT NULL,
 archive_canonical BLOB NOT NULL CHECK(length(archive_canonical)<=16384),
 canonical BLOB NOT NULL CHECK(length(canonical)<=65536),
 UNIQUE(endpoint,bucket,prefix)
);
CREATE TABLE data_engine_transition_events(
 transition_id TEXT NOT NULL REFERENCES data_engine_transition_inputs(id),
 sequence INTEGER NOT NULL CHECK(sequence>0),
 canonical BLOB NOT NULL CHECK(length(canonical)<=65536),
 PRIMARY KEY(transition_id,sequence)
);
CREATE TABLE data_native_epoch_materials(
 binding_id TEXT NOT NULL REFERENCES data_replica_bindings(id),
 epoch_id TEXT NOT NULL REFERENCES data_native_epochs(epoch_id),
 runtime_version INTEGER NOT NULL CHECK(runtime_version>0),
 canonical BLOB NOT NULL CHECK(length(canonical)<=65536),
 PRIMARY KEY(binding_id,epoch_id,runtime_version),
 FOREIGN KEY(binding_id,runtime_version) REFERENCES data_engine_runtime_commits(binding_id,version)
);
UPDATE schema_version SET version=16;
`)
	if err != nil {
		return err
	}
	return nativeTransitionImmutability(ctx, tx)
}

func nativeTransitionImmutability(ctx context.Context, tx *sql.Tx) error {
	for _, table := range []string{"data_engine_transition_inputs", "data_native_epochs", "data_engine_transition_events", "data_native_epoch_materials"} {
		if err := nativeTransitionTriggers(ctx, tx, table); err != nil {
			return err
		}
	}
	return nil
}

func nativeTransitionTriggers(ctx context.Context, tx *sql.Tx, table string) error {
	for _, action := range []string{"UPDATE", "DELETE"} {
		statement := "CREATE TRIGGER " + table + "_no_" + action + " BEFORE " + action + " ON " + table + " BEGIN SELECT RAISE(ABORT,'immutable native transition'); END;"
		if _, err := tx.ExecContext(ctx, statement); err != nil {
			return err
		}
	}
	return nil
}
