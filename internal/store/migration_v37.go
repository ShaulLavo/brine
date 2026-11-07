package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"
)

// v37 is a local candidate. Frozen migrations remain unchanged.
func migrateSQLiteRemoval(ctx context.Context, tx *sql.Tx) error {
	if _, err := tx.ExecContext(ctx, `
CREATE TABLE operations_v37 (
 id TEXT PRIMARY KEY, plan_id TEXT REFERENCES plans(id), requester TEXT NOT NULL, idempotency_key TEXT NOT NULL,
 state TEXT NOT NULL CHECK(state IN ('queued','launch_unknown','preflight','preparing','quiescing','starting','checking','committing','rolling_back','succeeded','failed','rolled_back','recovery_required')),
 created_at TEXT NOT NULL, updated_at TEXT NOT NULL,
 kind TEXT NOT NULL CHECK(kind IN ('deploy','secret_set','reconcile','resolve','restore_test','credential_activation','data_init_apply','native_capture','native_restore_test','native_prepare','native_prepare_abandon','module_publish_apply','module_publish_resolve','native_live_restore','native_live_restore_resolve','native_retire','native_retire_resolve','native_recovery_set','native_backup_activate','native_material_set','cdn','native_backup_resolve','archive_maintenance_apply','archive_maintenance_resolve','sqlite_prepare','sqlite_prepare_abandon','drop_upload_prepare','drop_apply','drop_resolve','sqlite_retire','sqlite_retire_resolve')),
 app TEXT NOT NULL, secret_ref TEXT NOT NULL, recovery_of TEXT NOT NULL DEFAULT '',
 CHECK((kind IN ('drop_upload_prepare','drop_apply','drop_resolve') AND plan_id IS NULL AND app='' AND length(secret_ref)=71 AND substr(secret_ref,1,7)='sha256:' AND substr(secret_ref,8) NOT GLOB '*[^0-9a-f]*' AND ((kind='drop_resolve' AND recovery_of<>'') OR (kind<>'drop_resolve' AND recovery_of='')) AND state IN ('queued','launch_unknown','preflight','succeeded','failed','recovery_required')) OR (kind IN ('restore_test','credential_activation','data_init_apply','native_capture','native_restore_test','native_prepare','native_prepare_abandon','module_publish_apply','native_live_restore','native_retire','native_recovery_set','native_backup_activate','archive_maintenance_apply') AND plan_id IS NULL AND app<>'' AND secret_ref<>'' AND recovery_of='' AND state IN ('queued','launch_unknown','preflight','succeeded','failed','recovery_required')) OR (kind IN ('sqlite_prepare','sqlite_prepare_abandon','sqlite_retire','sqlite_retire_resolve') AND plan_id IS NULL AND app<>'' AND length(secret_ref)=64 AND secret_ref NOT GLOB '*[^0-9a-f]*' AND ((kind='sqlite_retire_resolve' AND recovery_of<>'') OR (kind<>'sqlite_retire_resolve' AND recovery_of='')) AND state IN ('queued','launch_unknown','preflight','succeeded','failed','recovery_required')) OR (kind IN ('module_publish_resolve','native_live_restore_resolve','native_retire_resolve','native_backup_resolve','archive_maintenance_resolve') AND plan_id IS NULL AND app<>'' AND secret_ref<>'' AND recovery_of<>'' AND state IN ('queued','launch_unknown','preflight','succeeded','failed','recovery_required')) OR (kind='deploy' AND plan_id IS NOT NULL AND secret_ref='' AND recovery_of='') OR (kind='resolve' AND recovery_of<>'' AND ((plan_id IS NOT NULL AND secret_ref='') OR (plan_id IS NULL AND secret_ref<>''))) OR (kind IN ('secret_set','native_material_set','cdn') AND plan_id IS NULL AND secret_ref<>'' AND recovery_of='' AND state IN ('queued','preparing','succeeded','failed','recovery_required')) OR (kind='reconcile' AND plan_id IS NULL AND app='' AND secret_ref='' AND recovery_of='' AND state IN ('queued','launch_unknown','preflight','succeeded','failed','recovery_required'))),
 UNIQUE(requester,idempotency_key));
INSERT INTO operations_v37 SELECT * FROM operations;
DROP TABLE operations;
PRAGMA legacy_alter_table=ON;
ALTER TABLE operations_v37 RENAME TO operations;
PRAGMA legacy_alter_table=OFF;
CREATE TRIGGER legal_transition BEFORE UPDATE OF state ON operations WHEN OLD.state<>NEW.state AND NOT EXISTS (SELECT 1 FROM transitions WHERE kind=OLD.kind AND from_state=OLD.state AND to_state=NEW.state) BEGIN SELECT RAISE(ABORT,'illegal state transition'); END;
CREATE TRIGGER operation_identity BEFORE UPDATE OF id,plan_id,requester,idempotency_key,kind,app,secret_ref,recovery_of ON operations BEGIN SELECT RAISE(ABORT,'immutable operation identity'); END;
CREATE UNIQUE INDEX unresolved_source ON operations(recovery_of) WHERE kind IN ('sqlite_retire_resolve','resolve','module_publish_resolve','native_live_restore_resolve','native_retire_resolve','native_backup_resolve','archive_maintenance_resolve') AND state NOT IN ('failed','recovery_required');
CREATE UNIQUE INDEX archive_maintenance_attempt ON operations(secret_ref) WHERE kind='archive_maintenance_apply';
CREATE TRIGGER cdn_reserved_app BEFORE INSERT ON operations WHEN NEW.kind<>'cdn' AND EXISTS(SELECT 1 FROM cdn_reservations WHERE app=NEW.app) BEGIN SELECT RAISE(ABORT,'cdn app reserved'); END;

`); err != nil {
		return err
	}
	for _, table := range []string{"data_sqlite_point_consumers", "data_sqlite_consumer_completions"} {
		if err := rebuildSQLiteConsumerTable(ctx, tx, table); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, sqliteRemovalSchema); err != nil {
		return err
	}
	if err := extendSQLiteConsumerTrigger(ctx, tx, "data_sqlite_point_consumers_identity", sqliteArchiveConsumerSQL); err != nil {
		return err
	}
	if err := extendSQLiteConsumerTrigger(ctx, tx, "data_sqlite_consumer_completions_exact", sqliteArchiveCompletionSQL); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, "UPDATE schema_version SET version=37")
	return err
}

// Preserve each existing branch and trigger byte-for-byte except the new arm.
func extendSQLiteConsumerTrigger(ctx context.Context, tx *sql.Tx, name, branch string) error {
	var original string
	if err := tx.QueryRowContext(ctx, "SELECT sql FROM sqlite_master WHERE type='trigger' AND name=?", name).Scan(&original); err != nil {
		return err
	}
	at := strings.Index(original, "WHEN NOT EXISTS (")
	if at < 0 {
		return &IntegrityError{}
	}
	tail := original[at+len("WHEN NOT "):]
	end := strings.Index(tail, "\n OR EXISTS")
	if end < 0 {
		end = strings.Index(tail, "\nBEGIN")
	}
	if end < 0 {
		return &IntegrityError{}
	}
	updated := original[:at] + "WHEN NOT (" + tail[:end] + " OR EXISTS (" + branch + "))" + tail[end:]
	_, err := tx.ExecContext(ctx, "DROP TRIGGER "+name+"; "+updated)
	return err
}

func rebuildSQLiteConsumerTable(ctx context.Context, tx *sql.Tx, table string) error {
	var ddl string
	if err := tx.QueryRowContext(ctx, "SELECT sql FROM sqlite_master WHERE type='table' AND name=?", table).Scan(&ddl); err != nil {
		return err
	}
	start := strings.Index(ddl, "(")
	if start < 0 {
		return &IntegrityError{}
	}
	ddl = "CREATE TABLE " + table + "_v37 " + ddl[start:]
	if strings.Count(ddl, "operation_id TEXT NOT NULL UNIQUE") != 1 {
		return &IntegrityError{}
	}
	ddl = strings.Replace(ddl, "operation_id TEXT NOT NULL UNIQUE", "operation_id TEXT NOT NULL", 1)
	rows, err := tx.QueryContext(ctx, "SELECT sql FROM sqlite_master WHERE tbl_name=? AND type IN ('trigger','index') AND sql IS NOT NULL ORDER BY name", table)
	if err != nil {
		return err
	}
	var attached []string
	for rows.Next() {
		var statement string
		if err = rows.Scan(&statement); err != nil {
			return errors.Join(err, rows.Close())
		}
		attached = append(attached, statement)
	}
	if err = errors.Join(rows.Err(), rows.Close()); err != nil {
		return err
	}
	copySQL, err := sqliteConsumerRebuildSQL(table)
	if err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, "PRAGMA legacy_alter_table=ON"); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, ddl); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, copySQL); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, "PRAGMA legacy_alter_table=OFF"); err != nil {
		return err
	}
	for _, statement := range attached {
		if _, err = tx.ExecContext(ctx, statement); err != nil {
			return err
		}
	}
	return nil
}

const sqliteRemovalSchema = `
CREATE TABLE data_sqlite_retire_inputs(
 id TEXT PRIMARY KEY CHECK(length(id)=64 AND id NOT GLOB '*[^0-9a-f]*'),
 kind TEXT NOT NULL CHECK(kind IN ('sqlite_retire','sqlite_retire_resolve')),
 app TEXT NOT NULL, plan_id TEXT NOT NULL REFERENCES data_archive_plans(id),
 original_operation_id TEXT NOT NULL, disposition TEXT NOT NULL CHECK(disposition IN ('','continue','abandon')),
 canonical BLOB NOT NULL CHECK(length(canonical)<=4096),
 CHECK((kind='sqlite_retire' AND original_operation_id='' AND disposition='') OR (kind='sqlite_retire_resolve' AND original_operation_id<>'' AND disposition<>'')));
CREATE TRIGGER data_sqlite_retire_inputs_no_update BEFORE UPDATE ON data_sqlite_retire_inputs BEGIN SELECT RAISE(ABORT,'immutable SQLite retire input'); END;
CREATE TRIGGER data_sqlite_retire_inputs_no_delete BEFORE DELETE ON data_sqlite_retire_inputs BEGIN SELECT RAISE(ABORT,'immutable SQLite retire input'); END;
CREATE TABLE data_sqlite_archive_attempts(
 archive_operation_id TEXT PRIMARY KEY REFERENCES data_archive_operations(id),
 task_operation_id TEXT NOT NULL UNIQUE REFERENCES operations(id));
CREATE TRIGGER data_sqlite_archive_attempts_no_update BEFORE UPDATE ON data_sqlite_archive_attempts BEGIN SELECT RAISE(ABORT,'immutable SQLite archive attempt'); END;
CREATE TRIGGER data_sqlite_archive_attempts_no_delete BEFORE DELETE ON data_sqlite_archive_attempts BEGIN SELECT RAISE(ABORT,'immutable SQLite archive attempt'); END;
CREATE TRIGGER data_sqlite_archive_attempts_exact BEFORE INSERT ON data_sqlite_archive_attempts
WHEN EXISTS(SELECT 1 FROM data_sqlite_archive_attempts WHERE archive_operation_id=NEW.archive_operation_id OR task_operation_id=NEW.task_operation_id) OR NOT EXISTS(SELECT 1 FROM data_archive_operations a JOIN data_archive_plans p ON p.id=a.plan_id
 JOIN operations o ON o.id=NEW.task_operation_id JOIN data_sqlite_retire_inputs i ON i.id=o.secret_ref
 WHERE a.id=NEW.archive_operation_id AND o.kind='sqlite_retire' AND o.state='preflight'
 AND i.kind=o.kind AND i.app=o.app AND i.plan_id=p.id AND i.original_operation_id='' AND i.disposition=''
 AND o.app=json_extract(p.canonical,'$.candidate.app') AND o.requester=json_extract(p.canonical,'$.requester')
 AND json_type(p.canonical,'$.removal')='object'
 AND NOT EXISTS(SELECT 1 FROM data_archive_events WHERE operation_id=a.id))
BEGIN SELECT RAISE(ABORT,'SQLite archive attempt is not exact'); END;
CREATE TABLE data_sqlite_archive_claims(
 preparation_id TEXT PRIMARY KEY REFERENCES data_sqlite_preparations(id),
 archive_operation_id TEXT NOT NULL REFERENCES data_archive_operations(id),
 task_operation_id TEXT NOT NULL REFERENCES operations(id),
 database_id TEXT NOT NULL REFERENCES data_databases(id),
 fence_id TEXT NOT NULL UNIQUE REFERENCES data_fences(id),
 original_fence_owner TEXT NOT NULL, prepared_sha256 TEXT NOT NULL,
 canonical BLOB NOT NULL CHECK(length(canonical)<=4096),
 UNIQUE(archive_operation_id,database_id));
CREATE TRIGGER data_sqlite_archive_claims_no_update BEFORE UPDATE ON data_sqlite_archive_claims BEGIN SELECT RAISE(ABORT,'immutable SQLite archive claim'); END;
CREATE TRIGGER data_sqlite_archive_claims_no_delete BEFORE DELETE ON data_sqlite_archive_claims BEGIN SELECT RAISE(ABORT,'immutable SQLite archive claim'); END;
CREATE TRIGGER data_sqlite_archive_claims_exact BEFORE INSERT ON data_sqlite_archive_claims
WHEN EXISTS(SELECT 1 FROM data_sqlite_archive_claims WHERE preparation_id=NEW.preparation_id OR fence_id=NEW.fence_id OR (archive_operation_id=NEW.archive_operation_id AND database_id=NEW.database_id)) OR NOT EXISTS (
 SELECT 1 FROM data_sqlite_preparations p JOIN data_sqlite_prepared_points pt ON pt.preparation_id=p.id
 JOIN data_fences f ON f.id=p.fence_id JOIN operations previous ON previous.id=p.operation_id
 JOIN operations o ON o.id=NEW.task_operation_id JOIN data_sqlite_retire_inputs i ON i.id=o.secret_ref
 JOIN data_sqlite_archive_attempts attempt ON attempt.archive_operation_id=NEW.archive_operation_id AND attempt.task_operation_id=NEW.task_operation_id
 JOIN data_archive_operations a ON a.id=attempt.archive_operation_id JOIN data_archive_plans plan ON plan.id=a.plan_id
 JOIN json_each(plan.canonical,'$.removal.points') ref
 WHERE p.id=NEW.preparation_id AND previous.state='succeeded' AND f.state='held'
 AND f.database_id=NEW.database_id AND p.database_id=NEW.database_id AND f.id=NEW.fence_id AND f.operation_id=NEW.original_fence_owner
 AND o.kind='sqlite_retire' AND o.state='preflight' AND i.kind=o.kind AND i.app=o.app AND i.plan_id=a.plan_id
 AND o.app=json_extract(plan.canonical,'$.candidate.app') AND o.requester=json_extract(plan.canonical,'$.requester')
 AND o.requester=json_extract(p.canonical,'$.task.requester') AND o.app=json_extract(p.canonical,'$.task.app')
 AND json_extract(ref.value,'$.preparation_id')=p.id AND json_extract(ref.value,'$.database_id')=p.database_id
 AND json_extract(ref.value,'$.prepared_sha256')=NEW.prepared_sha256 AND json_extract(ref.value,'$.fence.id')=f.id
 AND json_extract(NEW.canonical,'$.preparation_id')=p.id AND json_extract(NEW.canonical,'$.archive_operation_id')=a.id
 AND json_extract(NEW.canonical,'$.task_operation_id')=o.id AND json_extract(NEW.canonical,'$.database_id')=p.database_id
 AND json_extract(NEW.canonical,'$.fence_id')=f.id AND json_extract(NEW.canonical,'$.original_fence_owner')=f.operation_id
 AND json_extract(NEW.canonical,'$.prepared_sha256')=NEW.prepared_sha256)
 OR EXISTS(SELECT 1 FROM data_sqlite_point_consumers WHERE preparation_id=NEW.preparation_id)
BEGIN SELECT RAISE(ABORT,'SQLite archive claim is not exact'); END;
CREATE TABLE data_sqlite_archive_completions(
 archive_id TEXT PRIMARY KEY REFERENCES data_archive_plans(archive_id),
 attempt_operation_id TEXT NOT NULL REFERENCES operations(id),
 completing_operation_id TEXT NOT NULL UNIQUE REFERENCES operations(id),
 outcome TEXT NOT NULL CHECK(outcome IN ('retired','abandoned')));
CREATE TRIGGER data_sqlite_archive_completions_no_update BEFORE UPDATE ON data_sqlite_archive_completions BEGIN SELECT RAISE(ABORT,'immutable SQLite archive completion'); END;
CREATE TRIGGER data_sqlite_archive_completions_no_delete BEFORE DELETE ON data_sqlite_archive_completions BEGIN SELECT RAISE(ABORT,'immutable SQLite archive completion'); END;
CREATE TRIGGER data_sqlite_archive_completions_exact BEFORE INSERT ON data_sqlite_archive_completions
WHEN EXISTS(SELECT 1 FROM data_sqlite_archive_completions WHERE archive_id=NEW.archive_id OR completing_operation_id=NEW.completing_operation_id) OR NOT EXISTS (
 SELECT 1 FROM data_archive_plans p JOIN data_archive_operations a ON a.plan_id=p.id
 JOIN data_sqlite_archive_attempts attempt ON attempt.archive_operation_id=a.id AND attempt.task_operation_id=NEW.attempt_operation_id
 JOIN operations original ON original.id=attempt.task_operation_id JOIN data_sqlite_retire_inputs input ON input.id=original.secret_ref
 JOIN operations finishing ON finishing.id=NEW.completing_operation_id JOIN data_sqlite_retire_inputs finput ON finput.id=finishing.secret_ref
 WHERE p.archive_id=NEW.archive_id AND original.kind='sqlite_retire' AND input.plan_id=p.id
 AND original.app=json_extract(p.canonical,'$.candidate.app') AND original.requester=json_extract(p.canonical,'$.requester')
 AND finishing.state='preflight' AND finishing.requester=original.requester AND finishing.app=original.app AND finput.plan_id=p.id
 AND ((finishing.id=original.id AND original.state='preflight' AND finput.kind='sqlite_retire')
 OR (finishing.kind='sqlite_retire_resolve' AND original.state='recovery_required' AND finishing.recovery_of=original.id AND finput.original_operation_id=original.id
 AND finput.disposition=CASE NEW.outcome WHEN 'retired' THEN 'continue' ELSE 'abandon' END)))
BEGIN SELECT RAISE(ABORT,'SQLite archive completion is not exact'); END;
CREATE TRIGGER data_sqlite_point_consumers_task_exclusive BEFORE INSERT ON data_sqlite_point_consumers
WHEN EXISTS (SELECT 1 FROM data_sqlite_point_consumers prior WHERE prior.operation_id=NEW.operation_id
 AND (NEW.kind<>'reviewed_mutation' OR prior.kind<>'reviewed_mutation'
 OR NOT EXISTS(SELECT 1 FROM data_sqlite_archive_claims a JOIN data_sqlite_archive_claims b ON b.archive_operation_id=a.archive_operation_id AND b.task_operation_id=a.task_operation_id
 JOIN operations o ON o.id=a.task_operation_id WHERE a.preparation_id=NEW.preparation_id AND b.preparation_id=prior.preparation_id AND o.id=NEW.operation_id AND o.kind='sqlite_retire')))
BEGIN SELECT RAISE(ABORT,'SQLite non-archive task already consumed'); END;
CREATE TRIGGER data_sqlite_consumer_completions_task_exclusive BEFORE INSERT ON data_sqlite_consumer_completions
WHEN EXISTS (SELECT 1 FROM data_sqlite_consumer_completions prior WHERE prior.operation_id=NEW.operation_id
 AND NOT EXISTS(SELECT 1 FROM data_sqlite_archive_claims a JOIN data_sqlite_archive_claims b ON b.archive_operation_id=a.archive_operation_id AND b.task_operation_id=a.task_operation_id
 JOIN data_archive_operations o ON o.id=a.archive_operation_id JOIN data_archive_plans p ON p.id=o.plan_id
 JOIN data_sqlite_archive_completions c ON c.archive_id=p.archive_id
 WHERE a.preparation_id=NEW.preparation_id AND b.preparation_id=prior.preparation_id AND c.completing_operation_id=NEW.operation_id AND c.attempt_operation_id=a.task_operation_id AND c.outcome='abandoned'))
BEGIN SELECT RAISE(ABORT,'SQLite non-archive task already completed'); END;
CREATE TABLE data_sqlite_archive_recovery(
 task_operation_id TEXT PRIMARY KEY REFERENCES operations(id),
 archive_operation_id TEXT NOT NULL REFERENCES data_archive_operations(id),
 attempt_operation_id TEXT NOT NULL REFERENCES operations(id),
 canonical BLOB NOT NULL CHECK(length(canonical)<=131072));
CREATE TRIGGER data_sqlite_archive_recovery_no_update BEFORE UPDATE ON data_sqlite_archive_recovery BEGIN SELECT RAISE(ABORT,'immutable SQLite archive recovery'); END;
CREATE TRIGGER data_sqlite_archive_recovery_no_delete BEFORE DELETE ON data_sqlite_archive_recovery BEGIN SELECT RAISE(ABORT,'immutable SQLite archive recovery'); END;
CREATE TRIGGER data_sqlite_archive_recovery_exact BEFORE INSERT ON data_sqlite_archive_recovery
WHEN EXISTS(SELECT 1 FROM data_sqlite_archive_recovery WHERE task_operation_id=NEW.task_operation_id)
 OR NOT EXISTS(SELECT 1 FROM data_sqlite_archive_attempts a JOIN operations original ON original.id=a.task_operation_id
 JOIN operations resolving ON resolving.id=NEW.task_operation_id JOIN data_sqlite_retire_inputs i ON i.id=resolving.secret_ref
 JOIN data_archive_operations operation ON operation.id=a.archive_operation_id JOIN data_archive_plans p ON p.id=operation.plan_id
 WHERE a.archive_operation_id=NEW.archive_operation_id AND a.task_operation_id=NEW.attempt_operation_id
 AND original.kind='sqlite_retire' AND original.state='recovery_required' AND resolving.kind='sqlite_retire_resolve' AND resolving.state='preflight'
 AND resolving.recovery_of=original.id AND resolving.app=original.app AND resolving.requester=original.requester
 AND i.kind=resolving.kind AND i.plan_id=p.id AND i.original_operation_id=original.id AND i.disposition='continue'
 AND json_extract(NEW.canonical,'$.task_operation_id')=resolving.id
 AND json_extract(NEW.canonical,'$.archive_operation_id')=operation.id
 AND json_extract(NEW.canonical,'$.attempt_operation_id')=original.id
 AND json_extract(NEW.canonical,'$.plan_id')=p.id)
BEGIN SELECT RAISE(ABORT,'SQLite archive recovery is not exact'); END;
CREATE TRIGGER data_fences_sqlite_archive_release BEFORE UPDATE OF state ON data_fences
WHEN OLD.state='held' AND NEW.state<>'held'
 AND EXISTS(SELECT 1 FROM data_archive_fences f JOIN data_sqlite_archive_attempts a ON a.archive_operation_id=f.operation_id WHERE f.fence_id=OLD.id)
 AND NOT EXISTS(SELECT 1 FROM data_archive_fences f JOIN data_sqlite_archive_attempts a ON a.archive_operation_id=f.operation_id
 JOIN data_archive_operations operation ON operation.id=a.archive_operation_id JOIN data_archive_plans p ON p.id=operation.plan_id
 JOIN data_sqlite_archive_completions done ON done.archive_id=p.archive_id AND done.attempt_operation_id=a.task_operation_id AND done.outcome='abandoned'
 WHERE f.fence_id=OLD.id)
BEGIN SELECT RAISE(ABORT,'SQLite archive protects original fence'); END;
CREATE TABLE data_sqlite_retire_settlements(operation_id TEXT PRIMARY KEY REFERENCES operations(id), code TEXT NOT NULL);
CREATE TRIGGER data_sqlite_retire_settlements_no_update BEFORE UPDATE ON data_sqlite_retire_settlements BEGIN SELECT RAISE(ABORT,'immutable SQLite retire settlement'); END;
CREATE TRIGGER data_sqlite_retire_settlements_no_delete BEFORE DELETE ON data_sqlite_retire_settlements BEGIN SELECT RAISE(ABORT,'immutable SQLite retire settlement'); END;
`

const sqliteArchiveConsumerSQL = `
 SELECT 1 FROM data_sqlite_archive_claims a JOIN data_sqlite_preparations p ON p.id=a.preparation_id
 JOIN operations o ON o.id=a.task_operation_id JOIN data_sqlite_retire_inputs i ON i.id=o.secret_ref
 JOIN data_archive_operations operation ON operation.id=a.archive_operation_id JOIN data_archive_plans plan ON plan.id=operation.plan_id
 WHERE a.preparation_id=NEW.preparation_id AND a.task_operation_id=NEW.operation_id AND NEW.kind='reviewed_mutation' AND NEW.successor_id IS NULL
 AND o.kind='sqlite_retire' AND o.state='preflight' AND i.kind=o.kind AND i.app=o.app AND i.plan_id=plan.id
 AND o.requester=json_extract(p.canonical,'$.task.requester') AND o.requester=json_extract(plan.canonical,'$.requester')
 AND json_extract(NEW.canonical,'$.preparation_id')=a.preparation_id AND json_extract(NEW.canonical,'$.operation_id')=o.id
 AND json_extract(NEW.canonical,'$.kind')='reviewed_mutation' AND json_extract(NEW.canonical,'$.app')=o.app
 AND json_extract(NEW.canonical,'$.requester')=o.requester AND json_extract(NEW.canonical,'$.input_sha256')=o.secret_ref
 AND json_extract(NEW.canonical,'$.prepared_sha256')=a.prepared_sha256
 AND 'sha256:'||json_extract(NEW.canonical,'$.reviewed_plan_sha256')=plan.id
 AND 'sha256:'||json_extract(NEW.canonical,'$.policy_sha256')=json_extract(plan.canonical,'$.policy_hash')
 AND json_extract(NEW.canonical,'$.bounds')=json_extract(plan.canonical,'$.removal.bounds')
 AND EXISTS(SELECT 1 FROM operations previous WHERE previous.id=p.operation_id AND previous.state='succeeded')`

const sqliteArchiveCompletionSQL = `
 SELECT 1 FROM data_sqlite_archive_claims a JOIN data_sqlite_point_consumers c ON c.preparation_id=a.preparation_id
 JOIN data_archive_operations operation ON operation.id=a.archive_operation_id JOIN data_archive_plans p ON p.id=operation.plan_id
 JOIN data_sqlite_archive_completions done ON done.archive_id=p.archive_id
 WHERE a.preparation_id=NEW.preparation_id AND a.fence_id=NEW.fence_id AND c.kind='reviewed_mutation' AND c.operation_id=a.task_operation_id
 AND done.attempt_operation_id=a.task_operation_id AND done.completing_operation_id=NEW.operation_id AND done.outcome='abandoned'`

func sqliteConsumerRebuildSQL(table string) (string, error) {
	switch table {
	case "data_sqlite_point_consumers":
		return "INSERT INTO data_sqlite_point_consumers_v37 SELECT * FROM data_sqlite_point_consumers; DROP TABLE data_sqlite_point_consumers; ALTER TABLE data_sqlite_point_consumers_v37 RENAME TO data_sqlite_point_consumers", nil
	case "data_sqlite_consumer_completions":
		return "INSERT INTO data_sqlite_consumer_completions_v37 SELECT * FROM data_sqlite_consumer_completions; DROP TABLE data_sqlite_consumer_completions; ALTER TABLE data_sqlite_consumer_completions_v37 RENAME TO data_sqlite_consumer_completions", nil
	default:
		return "", ErrInvalid
	}
}
