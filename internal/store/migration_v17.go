package store

import (
	"context"
	"database/sql"

	"github.com/ShaulLavo/brine/internal/ops"
)

func migrateNativeCapture(ctx context.Context, tx *sql.Tx) error {
	const migration = `
CREATE TABLE operations_v17 (
 id TEXT PRIMARY KEY, plan_id TEXT REFERENCES plans(id), requester TEXT NOT NULL, idempotency_key TEXT NOT NULL,
 state TEXT NOT NULL CHECK(state IN ('queued','launch_unknown','preflight','preparing','quiescing','starting','checking','committing','rolling_back','succeeded','failed','rolled_back','recovery_required')),
 created_at TEXT NOT NULL, updated_at TEXT NOT NULL,
 kind TEXT NOT NULL CHECK(kind IN ('deploy','secret_set','reconcile','resolve','restore_test','credential_activation','data_init_apply','native_capture','native_restore_test','native_prepare')), app TEXT NOT NULL, secret_ref TEXT NOT NULL,
 recovery_of TEXT NOT NULL DEFAULT '',
 CHECK((kind IN ('restore_test','credential_activation','data_init_apply','native_capture','native_restore_test','native_prepare') AND plan_id IS NULL AND app<>'' AND secret_ref<>'' AND recovery_of='' AND state IN ('queued','launch_unknown','preflight','succeeded','failed','recovery_required')) OR (kind='deploy' AND plan_id IS NOT NULL AND secret_ref='' AND recovery_of='') OR (kind='resolve' AND recovery_of<>'' AND ((plan_id IS NOT NULL AND secret_ref='') OR (plan_id IS NULL AND secret_ref<>''))) OR (kind='secret_set' AND plan_id IS NULL AND secret_ref<>'' AND recovery_of='' AND state IN ('queued','preparing','succeeded','failed','recovery_required')) OR (kind='reconcile' AND plan_id IS NULL AND app='' AND secret_ref='' AND recovery_of='' AND state IN ('queued','launch_unknown','preflight','succeeded','failed','recovery_required'))),
 UNIQUE(requester,idempotency_key));
INSERT INTO operations_v17 SELECT * FROM operations;
DROP TABLE operations;
ALTER TABLE operations_v17 RENAME TO operations;
CREATE TRIGGER legal_transition BEFORE UPDATE OF state ON operations WHEN OLD.state<>NEW.state AND NOT EXISTS (SELECT 1 FROM transitions WHERE kind=OLD.kind AND from_state=OLD.state AND to_state=NEW.state) BEGIN SELECT RAISE(ABORT,'illegal state transition'); END;
CREATE TRIGGER operation_identity BEFORE UPDATE OF id,plan_id,requester,idempotency_key,kind,app,secret_ref,recovery_of ON operations BEGIN SELECT RAISE(ABORT,'immutable operation identity'); END;
CREATE UNIQUE INDEX unresolved_source ON operations(recovery_of) WHERE kind='resolve' AND state NOT IN ('failed','recovery_required');
CREATE TABLE data_native_task_inputs(id TEXT PRIMARY KEY,requester TEXT NOT NULL,kind TEXT NOT NULL CHECK(kind IN ('native_capture','native_restore_test','native_prepare')),binding_id TEXT NOT NULL REFERENCES data_replica_bindings(id),canonical BLOB NOT NULL CHECK(length(canonical)<=4096));
CREATE TRIGGER data_native_task_inputs_no_update BEFORE UPDATE ON data_native_task_inputs BEGIN SELECT RAISE(ABORT,'immutable native task'); END;
CREATE TRIGGER data_native_task_inputs_no_delete BEFORE DELETE ON data_native_task_inputs BEGIN SELECT RAISE(ABORT,'durable native task'); END;
CREATE TABLE data_native_preparations(id TEXT PRIMARY KEY,operation_id TEXT NOT NULL UNIQUE REFERENCES operations(id),binding_id TEXT NOT NULL REFERENCES data_replica_bindings(id),epoch_id TEXT NOT NULL,fence_id TEXT NOT NULL REFERENCES data_fences(id),canonical BLOB NOT NULL CHECK(length(canonical)<=16384));
CREATE TRIGGER data_native_preparations_no_update BEFORE UPDATE ON data_native_preparations BEGIN SELECT RAISE(ABORT,'immutable native preparation'); END;
CREATE TRIGGER data_native_preparations_no_delete BEFORE DELETE ON data_native_preparations BEGIN SELECT RAISE(ABORT,'durable native preparation'); END;
CREATE TABLE data_native_preparation_events(point_id TEXT NOT NULL REFERENCES data_native_preparations(id),sequence INTEGER NOT NULL CHECK(sequence>0),stage TEXT NOT NULL CHECK(stage IN ('stop_intent','stopped','source','remote','verified','completed')),previous_sha256 TEXT NOT NULL,canonical BLOB NOT NULL CHECK(length(canonical)<=4194304),PRIMARY KEY(point_id,sequence));
CREATE TRIGGER data_native_preparation_events_order BEFORE INSERT ON data_native_preparation_events WHEN NEW.sequence<>COALESCE((SELECT MAX(sequence)+1 FROM data_native_preparation_events WHERE point_id=NEW.point_id),1) BEGIN SELECT RAISE(ABORT,'native preparation sequence'); END;
CREATE TRIGGER data_native_preparation_events_no_update BEFORE UPDATE ON data_native_preparation_events BEGIN SELECT RAISE(ABORT,'append-only native preparation'); END;
CREATE TRIGGER data_native_preparation_events_no_delete BEFORE DELETE ON data_native_preparation_events BEGIN SELECT RAISE(ABORT,'durable native preparation'); END;
CREATE TABLE data_native_prepared_points(id TEXT PRIMARY KEY REFERENCES data_native_preparations(id),binding_id TEXT NOT NULL REFERENCES data_replica_bindings(id),epoch_id TEXT NOT NULL,canonical BLOB NOT NULL CHECK(length(canonical)<=65536));
CREATE TRIGGER data_native_prepared_points_no_update BEFORE UPDATE ON data_native_prepared_points BEGIN SELECT RAISE(ABORT,'immutable native prepared point'); END;
CREATE TRIGGER data_native_prepared_points_no_delete BEFORE DELETE ON data_native_prepared_points BEGIN SELECT RAISE(ABORT,'retained native prepared point'); END;
CREATE TABLE data_native_verified_points(id TEXT PRIMARY KEY,operation_id TEXT NOT NULL UNIQUE REFERENCES operations(id),binding_id TEXT NOT NULL REFERENCES data_replica_bindings(id),epoch_id TEXT NOT NULL,canonical BLOB NOT NULL CHECK(length(canonical)<=65536));
CREATE TRIGGER data_native_verified_points_no_update BEFORE UPDATE ON data_native_verified_points BEGIN SELECT RAISE(ABORT,'immutable native verified point'); END;
CREATE TRIGGER data_native_verified_points_no_delete BEFORE DELETE ON data_native_verified_points BEGIN SELECT RAISE(ABORT,'retained native verified point'); END;
CREATE TABLE data_native_verified_point_bundles(point_id TEXT PRIMARY KEY REFERENCES data_native_verified_points(id),source BLOB NOT NULL CHECK(length(source)<=2097152),manifest BLOB NOT NULL CHECK(length(manifest)<=2097152),transport BLOB NOT NULL CHECK(length(transport)<=4096),source_evidence BLOB NOT NULL CHECK(length(source_evidence)<=1048576),restored_evidence BLOB NOT NULL CHECK(length(restored_evidence)<=1048576),confirmed_bound BLOB NOT NULL CHECK(length(confirmed_bound)<=1048576),final_pairs BLOB NOT NULL CHECK(length(final_pairs)<=1048576));
CREATE TRIGGER data_native_verified_point_bundles_no_update BEFORE UPDATE ON data_native_verified_point_bundles BEGIN SELECT RAISE(ABORT,'immutable native verified bundle'); END;
CREATE TRIGGER data_native_verified_point_bundles_no_delete BEFORE DELETE ON data_native_verified_point_bundles BEGIN SELECT RAISE(ABORT,'retained native verified bundle'); END;
CREATE TABLE data_native_point_consumers(point_id TEXT PRIMARY KEY REFERENCES data_native_prepared_points(id),kind TEXT NOT NULL CHECK(kind IN ('module_publish','live_restore','retire')),plan_id TEXT NOT NULL,apply_operation_id TEXT NOT NULL UNIQUE REFERENCES operations(id),requester TEXT NOT NULL,binding_id TEXT NOT NULL REFERENCES data_replica_bindings(id),epoch_id TEXT NOT NULL,generation INTEGER NOT NULL CHECK(generation>=0),generation_id TEXT NOT NULL,policy_sha256 TEXT NOT NULL,canonical BLOB NOT NULL CHECK(length(canonical)<=4096));
CREATE TRIGGER data_native_point_consumers_no_update BEFORE UPDATE ON data_native_point_consumers BEGIN SELECT RAISE(ABORT,'immutable native point consumer'); END;
CREATE TRIGGER data_native_point_consumers_no_delete BEFORE DELETE ON data_native_point_consumers BEGIN SELECT RAISE(ABORT,'durable native point consumer'); END;
CREATE TABLE data_native_repository_intents(id TEXT PRIMARY KEY,operation_id TEXT NOT NULL UNIQUE REFERENCES operations(id),binding_id TEXT NOT NULL REFERENCES data_replica_bindings(id),epoch_id TEXT NOT NULL UNIQUE,endpoint TEXT NOT NULL,bucket TEXT NOT NULL,prefix TEXT NOT NULL,canonical BLOB NOT NULL CHECK(length(canonical)<=32768),UNIQUE(endpoint,bucket,prefix));
CREATE TRIGGER data_native_repository_intents_no_update BEFORE UPDATE ON data_native_repository_intents BEGIN SELECT RAISE(ABORT,'immutable native repository intent'); END;
CREATE TRIGGER data_native_repository_intents_no_delete BEFORE DELETE ON data_native_repository_intents BEGIN SELECT RAISE(ABORT,'retained native repository ownership'); END;
CREATE TABLE data_native_repository_results(intent_id TEXT PRIMARY KEY REFERENCES data_native_repository_intents(id),repository_id TEXT NOT NULL,canonical BLOB NOT NULL CHECK(length(canonical)<=4096));
CREATE TRIGGER data_native_repository_results_no_update BEFORE UPDATE ON data_native_repository_results BEGIN SELECT RAISE(ABORT,'immutable native repository identity'); END;
CREATE TRIGGER data_native_repository_results_no_delete BEFORE DELETE ON data_native_repository_results BEGIN SELECT RAISE(ABORT,'retained native repository identity'); END;
UPDATE schema_version SET version=17;
`
	if _, err := tx.ExecContext(ctx, migration); err != nil {
		return err
	}
	for _, kind := range []ops.Kind{ops.NativeCapture, ops.NativeRestoreTest, ops.NativePrepare} {
		if err := insertNativeTaskTransitions(ctx, tx, kind); err != nil {
			return err
		}
	}
	return nil
}

func insertNativeTaskTransitions(ctx context.Context, tx *sql.Tx, kind ops.Kind) error {
	for from, targets := range ops.TransitionsFor(kind) {
		for _, to := range targets {
			if _, err := tx.ExecContext(ctx, "INSERT INTO transitions VALUES(?,?,?)", kind, from, to); err != nil {
				return err
			}
		}
	}
	return nil
}
