package store

import (
	"context"
	"database/sql"
)

func migrateCDNAttachments(ctx context.Context, tx *sql.Tx) error {
	_, err := tx.ExecContext(ctx, `
CREATE TABLE operations_v33 (
 id TEXT PRIMARY KEY, plan_id TEXT REFERENCES plans(id), requester TEXT NOT NULL, idempotency_key TEXT NOT NULL,
 state TEXT NOT NULL CHECK(state IN ('queued','launch_unknown','preflight','preparing','quiescing','starting','checking','committing','rolling_back','succeeded','failed','rolled_back','recovery_required')),
 created_at TEXT NOT NULL, updated_at TEXT NOT NULL,
 kind TEXT NOT NULL CHECK(kind IN ('deploy','secret_set','reconcile','resolve','restore_test','credential_activation','data_init_apply','native_capture','native_restore_test','native_prepare','native_prepare_abandon','module_publish_apply','module_publish_resolve','native_live_restore','native_live_restore_resolve','native_retire','native_retire_resolve','native_recovery_set','native_backup_activate','native_material_set','cdn','native_backup_resolve','archive_maintenance_apply','archive_maintenance_resolve')),
 app TEXT NOT NULL, secret_ref TEXT NOT NULL, recovery_of TEXT NOT NULL DEFAULT '',
 CHECK((kind IN ('restore_test','credential_activation','data_init_apply','native_capture','native_restore_test','native_prepare','native_prepare_abandon','module_publish_apply','native_live_restore','native_retire','native_recovery_set','native_backup_activate','archive_maintenance_apply') AND plan_id IS NULL AND app<>'' AND secret_ref<>'' AND recovery_of='' AND state IN ('queued','launch_unknown','preflight','succeeded','failed','recovery_required')) OR (kind IN ('module_publish_resolve','native_live_restore_resolve','native_retire_resolve','native_backup_resolve','archive_maintenance_resolve') AND plan_id IS NULL AND app<>'' AND secret_ref<>'' AND recovery_of<>'' AND state IN ('queued','launch_unknown','preflight','succeeded','failed','recovery_required')) OR (kind='deploy' AND plan_id IS NOT NULL AND secret_ref='' AND recovery_of='') OR (kind='resolve' AND recovery_of<>'' AND ((plan_id IS NOT NULL AND secret_ref='') OR (plan_id IS NULL AND secret_ref<>''))) OR (kind IN ('secret_set','native_material_set','cdn') AND plan_id IS NULL AND secret_ref<>'' AND recovery_of='' AND state IN ('queued','preparing','succeeded','failed','recovery_required')) OR (kind='reconcile' AND plan_id IS NULL AND app='' AND secret_ref='' AND recovery_of='' AND state IN ('queued','launch_unknown','preflight','succeeded','failed','recovery_required'))),
 UNIQUE(requester,idempotency_key));
INSERT INTO operations_v33 SELECT * FROM operations;
DROP TABLE operations;
ALTER TABLE operations_v33 RENAME TO operations;
CREATE TRIGGER legal_transition BEFORE UPDATE OF state ON operations WHEN OLD.state<>NEW.state AND NOT EXISTS (SELECT 1 FROM transitions WHERE kind=OLD.kind AND from_state=OLD.state AND to_state=NEW.state) BEGIN SELECT RAISE(ABORT,'illegal state transition'); END;
CREATE TRIGGER operation_identity BEFORE UPDATE OF id,plan_id,requester,idempotency_key,kind,app,secret_ref,recovery_of ON operations BEGIN SELECT RAISE(ABORT,'immutable operation identity'); END;
CREATE UNIQUE INDEX unresolved_source ON operations(recovery_of) WHERE kind IN ('resolve','module_publish_resolve','native_live_restore_resolve','native_retire_resolve','native_backup_resolve','archive_maintenance_resolve') AND state NOT IN ('failed','recovery_required');
CREATE UNIQUE INDEX archive_maintenance_attempt ON operations(secret_ref) WHERE kind='archive_maintenance_apply';

CREATE TABLE cdn_plans(id TEXT PRIMARY KEY,app TEXT NOT NULL,hostname TEXT NOT NULL,attachment_id TEXT NOT NULL,canonical BLOB NOT NULL CHECK(length(canonical)<=1048576));
CREATE TABLE cdn_sessions(operation_id TEXT PRIMARY KEY REFERENCES operations(id),plan_id TEXT NOT NULL REFERENCES cdn_plans(id),proof BLOB,readback BLOB);
CREATE TABLE cdn_effects(operation_id TEXT NOT NULL REFERENCES cdn_sessions(operation_id),step_index INTEGER NOT NULL CHECK(step_index>=0 AND step_index<16),permit BLOB NOT NULL,record BLOB NOT NULL,PRIMARY KEY(operation_id,step_index));
CREATE TABLE cdn_evidence(operation_id TEXT NOT NULL REFERENCES cdn_sessions(operation_id),seq INTEGER NOT NULL,kind TEXT NOT NULL CHECK(kind IN ('seal','result','inspection','qualification')),canonical BLOB NOT NULL,PRIMARY KEY(operation_id,seq));
CREATE TABLE cdn_reservations(app TEXT PRIMARY KEY,hostname TEXT NOT NULL UNIQUE,attachment_id TEXT NOT NULL UNIQUE,operation_id TEXT NOT NULL UNIQUE REFERENCES cdn_sessions(operation_id));
CREATE TABLE cdn_revisions(id TEXT PRIMARY KEY,app TEXT NOT NULL,attachment_id TEXT NOT NULL,canonical BLOB NOT NULL);
CREATE TABLE cdn_heads(app TEXT PRIMARY KEY,revision_id TEXT NOT NULL REFERENCES cdn_revisions(id));
CREATE TRIGGER cdn_plans_no_update BEFORE UPDATE ON cdn_plans BEGIN SELECT RAISE(ABORT,'immutable cdn plan'); END;
CREATE TRIGGER cdn_plans_no_delete BEFORE DELETE ON cdn_plans BEGIN SELECT RAISE(ABORT,'durable cdn plan'); END;
CREATE TRIGGER cdn_effects_no_permit_update BEFORE UPDATE OF operation_id,step_index,permit ON cdn_effects BEGIN SELECT RAISE(ABORT,'immutable cdn intent'); END;
CREATE TRIGGER cdn_effects_no_delete BEFORE DELETE ON cdn_effects BEGIN SELECT RAISE(ABORT,'durable cdn intent'); END;
CREATE TRIGGER cdn_evidence_no_update BEFORE UPDATE ON cdn_evidence BEGIN SELECT RAISE(ABORT,'immutable cdn evidence'); END;
CREATE TRIGGER cdn_evidence_no_delete BEFORE DELETE ON cdn_evidence BEGIN SELECT RAISE(ABORT,'durable cdn evidence'); END;
CREATE TRIGGER cdn_revisions_no_update BEFORE UPDATE ON cdn_revisions BEGIN SELECT RAISE(ABORT,'immutable cdn revision'); END;
CREATE TRIGGER cdn_revisions_no_delete BEFORE DELETE ON cdn_revisions BEGIN SELECT RAISE(ABORT,'durable cdn revision'); END;
CREATE TRIGGER cdn_reserved_app BEFORE INSERT ON operations WHEN NEW.kind<>'cdn' AND EXISTS(SELECT 1 FROM cdn_reservations WHERE app=NEW.app) BEGIN SELECT RAISE(ABORT,'cdn app reserved'); END;
UPDATE schema_version SET version=33;
`)
	return err
}
