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
 fence_id TEXT NOT NULL REFERENCES data_fences(id),
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
 stage TEXT NOT NULL CHECK(stage IN ('prepared','preserve_intent','original_preserved','promote_intent','target_promoted','activation_intent','activated','finish_intent','completed','abandon_intent','abandoned_current')),
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
	_, err := tx.ExecContext(ctx, `
CREATE TRIGGER data_engine_transition_inputs_no_update BEFORE UPDATE ON data_engine_transition_inputs BEGIN SELECT RAISE(ABORT,'immutable native transition'); END;
CREATE TRIGGER data_engine_transition_inputs_no_delete BEFORE DELETE ON data_engine_transition_inputs BEGIN SELECT RAISE(ABORT,'immutable native transition'); END;
CREATE TRIGGER data_native_epochs_no_update BEFORE UPDATE ON data_native_epochs BEGIN SELECT RAISE(ABORT,'immutable native transition'); END;
CREATE TRIGGER data_native_epochs_no_delete BEFORE DELETE ON data_native_epochs BEGIN SELECT RAISE(ABORT,'immutable native transition'); END;
CREATE TRIGGER data_engine_transition_events_no_update BEFORE UPDATE ON data_engine_transition_events BEGIN SELECT RAISE(ABORT,'immutable native transition'); END;
CREATE TRIGGER data_engine_transition_events_no_delete BEFORE DELETE ON data_engine_transition_events BEGIN SELECT RAISE(ABORT,'immutable native transition'); END;
CREATE TRIGGER data_native_epoch_materials_no_update BEFORE UPDATE ON data_native_epoch_materials BEGIN SELECT RAISE(ABORT,'immutable native transition'); END;
CREATE TRIGGER data_native_epoch_materials_no_delete BEFORE DELETE ON data_native_epoch_materials BEGIN SELECT RAISE(ABORT,'immutable native transition'); END;
`)
	return err
}
