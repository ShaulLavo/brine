package store

import (
	"context"
	"database/sql"
)

func migrateNativeMaterial(ctx context.Context, tx *sql.Tx) error {
	_, err := tx.ExecContext(ctx, `
CREATE TABLE operations_v23 (
 id TEXT PRIMARY KEY, plan_id TEXT REFERENCES plans(id), requester TEXT NOT NULL, idempotency_key TEXT NOT NULL,
 state TEXT NOT NULL CHECK(state IN ('queued','launch_unknown','preflight','preparing','quiescing','starting','checking','committing','rolling_back','succeeded','failed','rolled_back','recovery_required')),
 created_at TEXT NOT NULL, updated_at TEXT NOT NULL,
 kind TEXT NOT NULL CHECK(kind IN ('deploy','secret_set','reconcile','resolve','restore_test','credential_activation','data_init_apply','native_capture','native_restore_test','native_prepare','module_publish_apply','module_publish_resolve','native_live_restore','native_live_restore_resolve','native_retire','native_retire_resolve','native_recovery_set','native_backup_activate','native_material_set','native_backup_resolve')),
 app TEXT NOT NULL, secret_ref TEXT NOT NULL, recovery_of TEXT NOT NULL DEFAULT '',
 CHECK((kind IN ('restore_test','credential_activation','data_init_apply','native_capture','native_restore_test','native_prepare','module_publish_apply','native_live_restore','native_retire','native_recovery_set','native_backup_activate') AND plan_id IS NULL AND app<>'' AND secret_ref<>'' AND recovery_of='' AND state IN ('queued','launch_unknown','preflight','succeeded','failed','recovery_required')) OR (kind IN ('module_publish_resolve','native_live_restore_resolve','native_retire_resolve','native_backup_resolve') AND plan_id IS NULL AND app<>'' AND secret_ref<>'' AND recovery_of<>'' AND state IN ('queued','launch_unknown','preflight','succeeded','failed','recovery_required')) OR (kind='deploy' AND plan_id IS NOT NULL AND secret_ref='' AND recovery_of='') OR (kind='resolve' AND recovery_of<>'' AND ((plan_id IS NOT NULL AND secret_ref='') OR (plan_id IS NULL AND secret_ref<>''))) OR (kind IN ('secret_set','native_material_set') AND plan_id IS NULL AND secret_ref<>'' AND recovery_of='' AND state IN ('queued','preparing','succeeded','failed','recovery_required')) OR (kind='reconcile' AND plan_id IS NULL AND app='' AND secret_ref='' AND recovery_of='' AND state IN ('queued','launch_unknown','preflight','succeeded','failed','recovery_required'))),
 UNIQUE(requester,idempotency_key));
INSERT INTO operations_v23 SELECT * FROM operations;
DROP TABLE operations;
ALTER TABLE operations_v23 RENAME TO operations;
CREATE TRIGGER legal_transition BEFORE UPDATE OF state ON operations WHEN OLD.state<>NEW.state AND NOT EXISTS (SELECT 1 FROM transitions WHERE kind=OLD.kind AND from_state=OLD.state AND to_state=NEW.state) BEGIN SELECT RAISE(ABORT,'illegal state transition'); END;
CREATE TRIGGER operation_identity BEFORE UPDATE OF id,plan_id,requester,idempotency_key,kind,app,secret_ref,recovery_of ON operations BEGIN SELECT RAISE(ABORT,'immutable operation identity'); END;
CREATE UNIQUE INDEX unresolved_source ON operations(recovery_of) WHERE kind IN ('resolve','module_publish_resolve','native_live_restore_resolve','native_retire_resolve','native_backup_resolve') AND state NOT IN ('failed','recovery_required');
CREATE TABLE data_native_material_inputs(operation_id TEXT PRIMARY KEY REFERENCES operations(id),binding_id TEXT NOT NULL REFERENCES data_replica_bindings(id),version INTEGER NOT NULL CHECK(version>0),canonical BLOB NOT NULL CHECK(length(canonical)<=16384),UNIQUE(binding_id,version));
CREATE TRIGGER data_native_material_inputs_no_update BEFORE UPDATE ON data_native_material_inputs BEGIN SELECT RAISE(ABORT,'immutable material input'); END;
CREATE TRIGGER data_native_material_inputs_no_delete BEFORE DELETE ON data_native_material_inputs BEGIN SELECT RAISE(ABORT,'retained material allocation'); END;
CREATE TABLE data_native_material_receipts(operation_id TEXT PRIMARY KEY REFERENCES data_native_material_inputs(operation_id),canonical BLOB NOT NULL CHECK(length(canonical)<=8192));
CREATE TRIGGER data_native_material_receipts_no_update BEFORE UPDATE ON data_native_material_receipts BEGIN SELECT RAISE(ABORT,'immutable material receipt'); END;
CREATE TRIGGER data_native_material_receipts_no_delete BEFORE DELETE ON data_native_material_receipts BEGIN SELECT RAISE(ABORT,'retained material receipt'); END;
UPDATE schema_version SET version=23;
`)
	return err
}
