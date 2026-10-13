package store

import (
	"context"
	"database/sql"
)

func migrateArchiveMaintenanceTasks(ctx context.Context, tx *sql.Tx) error {
	if err := migrateWorkspaceRetirementJournal(ctx, tx); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, `
CREATE TABLE operations_v25 (
 id TEXT PRIMARY KEY, plan_id TEXT REFERENCES plans(id), requester TEXT NOT NULL, idempotency_key TEXT NOT NULL,
 state TEXT NOT NULL CHECK(state IN ('queued','launch_unknown','preflight','preparing','quiescing','starting','checking','committing','rolling_back','succeeded','failed','rolled_back','recovery_required')),
 created_at TEXT NOT NULL, updated_at TEXT NOT NULL,
 kind TEXT NOT NULL CHECK(kind IN ('deploy','secret_set','reconcile','resolve','restore_test','credential_activation','data_init_apply','native_capture','native_restore_test','native_prepare','module_publish_apply','module_publish_resolve','native_live_restore','native_live_restore_resolve','native_retire','native_retire_resolve','native_recovery_set','native_backup_activate','native_material_set','native_backup_resolve','archive_maintenance_apply','archive_maintenance_resolve')),
 app TEXT NOT NULL, secret_ref TEXT NOT NULL, recovery_of TEXT NOT NULL DEFAULT '',
 CHECK((kind IN ('restore_test','credential_activation','data_init_apply','native_capture','native_restore_test','native_prepare','module_publish_apply','native_live_restore','native_retire','native_recovery_set','native_backup_activate','archive_maintenance_apply') AND plan_id IS NULL AND app<>'' AND secret_ref<>'' AND recovery_of='' AND state IN ('queued','launch_unknown','preflight','succeeded','failed','recovery_required')) OR (kind IN ('module_publish_resolve','native_live_restore_resolve','native_retire_resolve','native_backup_resolve','archive_maintenance_resolve') AND plan_id IS NULL AND app<>'' AND secret_ref<>'' AND recovery_of<>'' AND state IN ('queued','launch_unknown','preflight','succeeded','failed','recovery_required')) OR (kind='deploy' AND plan_id IS NOT NULL AND secret_ref='' AND recovery_of='') OR (kind='resolve' AND recovery_of<>'' AND ((plan_id IS NOT NULL AND secret_ref='') OR (plan_id IS NULL AND secret_ref<>''))) OR (kind IN ('secret_set','native_material_set') AND plan_id IS NULL AND secret_ref<>'' AND recovery_of='' AND state IN ('queued','preparing','succeeded','failed','recovery_required')) OR (kind='reconcile' AND plan_id IS NULL AND app='' AND secret_ref='' AND recovery_of='' AND state IN ('queued','launch_unknown','preflight','succeeded','failed','recovery_required'))),
 UNIQUE(requester,idempotency_key));
INSERT INTO operations_v25 SELECT * FROM operations;
DROP TABLE operations;
ALTER TABLE operations_v25 RENAME TO operations;
CREATE TRIGGER legal_transition BEFORE UPDATE OF state ON operations WHEN OLD.state<>NEW.state AND NOT EXISTS (SELECT 1 FROM transitions WHERE kind=OLD.kind AND from_state=OLD.state AND to_state=NEW.state) BEGIN SELECT RAISE(ABORT,'illegal state transition'); END;
CREATE TRIGGER operation_identity BEFORE UPDATE OF id,plan_id,requester,idempotency_key,kind,app,secret_ref,recovery_of ON operations BEGIN SELECT RAISE(ABORT,'immutable operation identity'); END;
CREATE UNIQUE INDEX unresolved_source ON operations(recovery_of) WHERE kind IN ('resolve','module_publish_resolve','native_live_restore_resolve','native_retire_resolve','native_backup_resolve','archive_maintenance_resolve') AND state NOT IN ('failed','recovery_required');
CREATE UNIQUE INDEX archive_maintenance_attempt ON operations(secret_ref) WHERE kind='archive_maintenance_apply';
UPDATE schema_version SET version=25;
`)
	return err
}
