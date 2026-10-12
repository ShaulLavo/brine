package store

import (
	"context"
	"database/sql"
)

func migrateNativeLiveRestore(ctx context.Context, tx *sql.Tx) error {
	_, err := tx.ExecContext(ctx, `
CREATE TABLE data_native_live_restore_plans(
 id TEXT PRIMARY KEY,
 binding_id TEXT NOT NULL REFERENCES data_replica_bindings(id),
 prepared_point_id TEXT NOT NULL,
 selected_point_id TEXT NOT NULL,
 canonical BLOB NOT NULL CHECK(length(canonical)<=65536)
);
CREATE TABLE data_native_live_restore_claims(
 operation_id TEXT PRIMARY KEY REFERENCES operations(id),
 plan_id TEXT NOT NULL UNIQUE REFERENCES data_native_live_restore_plans(id),
 prepared_point_id TEXT NOT NULL UNIQUE,
 binding_id TEXT NOT NULL REFERENCES data_replica_bindings(id),
 incoming_epoch_id TEXT NOT NULL UNIQUE,
 transition_id TEXT NOT NULL UNIQUE,
 canonical BLOB NOT NULL CHECK(length(canonical)<=65536)
);
CREATE TABLE data_native_live_restore_events(
 operation_id TEXT NOT NULL REFERENCES data_native_live_restore_claims(operation_id),
 sequence INTEGER NOT NULL CHECK(sequence>0),
 stage TEXT NOT NULL CHECK(stage IN ('claimed','restore_intent','verified','transition_intent','transitioned','finish_intent','completed','abandon_intent','abandoned_current')),
 canonical BLOB NOT NULL CHECK(length(canonical)<=65536),
 PRIMARY KEY(operation_id,sequence)
);
CREATE TABLE data_native_live_restore_target_proofs(
 id TEXT PRIMARY KEY,
 operation_id TEXT NOT NULL UNIQUE REFERENCES data_native_live_restore_claims(operation_id),
 canonical BLOB NOT NULL CHECK(length(canonical)<=65536)
);
CREATE TABLE data_native_live_restore_completion_proofs(
 id TEXT PRIMARY KEY,
 operation_id TEXT NOT NULL UNIQUE REFERENCES data_native_live_restore_claims(operation_id),
 canonical BLOB NOT NULL CHECK(length(canonical)<=65536)
);
CREATE TRIGGER data_native_live_restore_plans_no_update BEFORE UPDATE ON data_native_live_restore_plans BEGIN SELECT RAISE(ABORT,'immutable live restore review'); END;
CREATE TRIGGER data_native_live_restore_plans_no_delete BEFORE DELETE ON data_native_live_restore_plans BEGIN SELECT RAISE(ABORT,'immutable live restore review'); END;
CREATE TRIGGER data_native_live_restore_claims_no_update BEFORE UPDATE ON data_native_live_restore_claims BEGIN SELECT RAISE(ABORT,'immutable live restore claim'); END;
CREATE TRIGGER data_native_live_restore_claims_no_delete BEFORE DELETE ON data_native_live_restore_claims BEGIN SELECT RAISE(ABORT,'immutable live restore claim'); END;
CREATE TRIGGER data_native_live_restore_events_no_update BEFORE UPDATE ON data_native_live_restore_events BEGIN SELECT RAISE(ABORT,'append-only live restore events'); END;
CREATE TRIGGER data_native_live_restore_events_no_delete BEFORE DELETE ON data_native_live_restore_events BEGIN SELECT RAISE(ABORT,'append-only live restore events'); END;
CREATE TRIGGER data_native_live_restore_target_proofs_no_update BEFORE UPDATE ON data_native_live_restore_target_proofs BEGIN SELECT RAISE(ABORT,'immutable live restore target'); END;
CREATE TRIGGER data_native_live_restore_target_proofs_no_delete BEFORE DELETE ON data_native_live_restore_target_proofs BEGIN SELECT RAISE(ABORT,'immutable live restore target'); END;
CREATE TRIGGER data_native_live_restore_completion_proofs_no_update BEFORE UPDATE ON data_native_live_restore_completion_proofs BEGIN SELECT RAISE(ABORT,'immutable live restore completion'); END;
CREATE TRIGGER data_native_live_restore_completion_proofs_no_delete BEFORE DELETE ON data_native_live_restore_completion_proofs BEGIN SELECT RAISE(ABORT,'immutable live restore completion'); END;
UPDATE schema_version SET version=20;
`)
	return err
}
