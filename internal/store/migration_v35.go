package store

import (
	"context"
	"database/sql"
)

// migrateSQLitePreparation adds the two closed SQLite preparation task kinds and
// their protected journal. The operations rebuild copies every row unchanged and
// recreates every index and trigger attached to operations, including
// cdn_reserved_app; migration 33's unrelated CDN tables are not recreated.
func migrateSQLitePreparation(ctx context.Context, tx *sql.Tx) error {
	_, err := tx.ExecContext(ctx, `
CREATE TABLE operations_v35 (
 id TEXT PRIMARY KEY, plan_id TEXT REFERENCES plans(id), requester TEXT NOT NULL, idempotency_key TEXT NOT NULL,
 state TEXT NOT NULL CHECK(state IN ('queued','launch_unknown','preflight','preparing','quiescing','starting','checking','committing','rolling_back','succeeded','failed','rolled_back','recovery_required')),
 created_at TEXT NOT NULL, updated_at TEXT NOT NULL,
 kind TEXT NOT NULL CHECK(kind IN ('deploy','secret_set','reconcile','resolve','restore_test','credential_activation','data_init_apply','native_capture','native_restore_test','native_prepare','native_prepare_abandon','module_publish_apply','module_publish_resolve','native_live_restore','native_live_restore_resolve','native_retire','native_retire_resolve','native_recovery_set','native_backup_activate','native_material_set','cdn','native_backup_resolve','archive_maintenance_apply','archive_maintenance_resolve','sqlite_prepare','sqlite_prepare_abandon')),
 app TEXT NOT NULL, secret_ref TEXT NOT NULL, recovery_of TEXT NOT NULL DEFAULT '',
 CHECK((kind IN ('restore_test','credential_activation','data_init_apply','native_capture','native_restore_test','native_prepare','native_prepare_abandon','module_publish_apply','native_live_restore','native_retire','native_recovery_set','native_backup_activate','archive_maintenance_apply') AND plan_id IS NULL AND app<>'' AND secret_ref<>'' AND recovery_of='' AND state IN ('queued','launch_unknown','preflight','succeeded','failed','recovery_required')) OR (kind IN ('sqlite_prepare','sqlite_prepare_abandon') AND plan_id IS NULL AND app<>'' AND length(secret_ref)=64 AND secret_ref NOT GLOB '*[^0-9a-f]*' AND recovery_of='' AND state IN ('queued','launch_unknown','preflight','succeeded','failed','recovery_required')) OR (kind IN ('module_publish_resolve','native_live_restore_resolve','native_retire_resolve','native_backup_resolve','archive_maintenance_resolve') AND plan_id IS NULL AND app<>'' AND secret_ref<>'' AND recovery_of<>'' AND state IN ('queued','launch_unknown','preflight','succeeded','failed','recovery_required')) OR (kind='deploy' AND plan_id IS NOT NULL AND secret_ref='' AND recovery_of='') OR (kind='resolve' AND recovery_of<>'' AND ((plan_id IS NOT NULL AND secret_ref='') OR (plan_id IS NULL AND secret_ref<>''))) OR (kind IN ('secret_set','native_material_set','cdn') AND plan_id IS NULL AND secret_ref<>'' AND recovery_of='' AND state IN ('queued','preparing','succeeded','failed','recovery_required')) OR (kind='reconcile' AND plan_id IS NULL AND app='' AND secret_ref='' AND recovery_of='' AND state IN ('queued','launch_unknown','preflight','succeeded','failed','recovery_required'))),
 UNIQUE(requester,idempotency_key));
INSERT INTO operations_v35 SELECT * FROM operations;
DROP TABLE operations;
ALTER TABLE operations_v35 RENAME TO operations;
CREATE TRIGGER legal_transition BEFORE UPDATE OF state ON operations WHEN OLD.state<>NEW.state AND NOT EXISTS (SELECT 1 FROM transitions WHERE kind=OLD.kind AND from_state=OLD.state AND to_state=NEW.state) BEGIN SELECT RAISE(ABORT,'illegal state transition'); END;
CREATE TRIGGER operation_identity BEFORE UPDATE OF id,plan_id,requester,idempotency_key,kind,app,secret_ref,recovery_of ON operations BEGIN SELECT RAISE(ABORT,'immutable operation identity'); END;
CREATE UNIQUE INDEX unresolved_source ON operations(recovery_of) WHERE kind IN ('resolve','module_publish_resolve','native_live_restore_resolve','native_retire_resolve','native_backup_resolve','archive_maintenance_resolve') AND state NOT IN ('failed','recovery_required');
CREATE UNIQUE INDEX archive_maintenance_attempt ON operations(secret_ref) WHERE kind='archive_maintenance_apply';
CREATE TRIGGER cdn_reserved_app BEFORE INSERT ON operations WHEN NEW.kind<>'cdn' AND EXISTS(SELECT 1 FROM cdn_reservations WHERE app=NEW.app) BEGIN SELECT RAISE(ABORT,'cdn app reserved'); END;

CREATE TABLE data_sqlite_task_inputs(
 id TEXT PRIMARY KEY CHECK(length(id)=64 AND id NOT GLOB '*[^0-9a-f]*'),
 kind TEXT NOT NULL CHECK(kind IN ('sqlite_prepare','sqlite_prepare_abandon')),
 app TEXT NOT NULL, database_id TEXT NOT NULL REFERENCES data_databases(id),
 preparation_id TEXT NOT NULL, supersedes TEXT,
 canonical BLOB NOT NULL CHECK(length(canonical)<=4096),
 CHECK(supersedes IS NULL OR kind='sqlite_prepare'));
CREATE TRIGGER data_sqlite_task_inputs_no_update BEFORE UPDATE ON data_sqlite_task_inputs BEGIN SELECT RAISE(ABORT,'immutable SQLite task input'); END;
CREATE TRIGGER data_sqlite_task_inputs_no_delete BEFORE DELETE ON data_sqlite_task_inputs BEGIN SELECT RAISE(ABORT,'immutable SQLite task input'); END;

CREATE TABLE data_sqlite_preparations(
 id TEXT PRIMARY KEY, operation_id TEXT NOT NULL UNIQUE REFERENCES operations(id),
 database_id TEXT NOT NULL REFERENCES data_databases(id),
 binding_id TEXT NOT NULL REFERENCES data_replica_bindings(id), epoch_id TEXT NOT NULL,
 fence_id TEXT NOT NULL REFERENCES data_fences(id),
 mode TEXT NOT NULL CHECK(mode IN ('fresh','supersede_live','supersede_reverify','supersede_capture')),
 arm TEXT NOT NULL CHECK(arm IN ('ltx','snapshot','allocated_empty')),
 supersedes TEXT UNIQUE REFERENCES data_sqlite_preparations(id),
 coverage_origin TEXT REFERENCES data_sqlite_preparations(id),
 baseline_from TEXT REFERENCES data_sqlite_preparations(id),
 canonical BLOB NOT NULL CHECK(length(canonical)<=131072),
 CHECK((mode='fresh')=(supersedes IS NULL)),
 CHECK(mode<>'supersede_reverify' OR (coverage_origin IS NOT NULL AND baseline_from IS NOT NULL)),
 CHECK(mode NOT IN ('fresh','supersede_live') OR (coverage_origin IS NULL AND baseline_from IS NULL)),
 CHECK(mode<>'supersede_capture' OR coverage_origin IS NULL),
 CHECK(mode<>'supersede_live' OR arm='ltx'),
 CHECK(mode<>'supersede_capture' OR arm<>'ltx'));
CREATE TRIGGER data_sqlite_preparations_no_update BEFORE UPDATE ON data_sqlite_preparations BEGIN SELECT RAISE(ABORT,'immutable SQLite preparation'); END;
CREATE TRIGGER data_sqlite_preparations_no_delete BEFORE DELETE ON data_sqlite_preparations BEGIN SELECT RAISE(ABORT,'immutable SQLite preparation'); END;
CREATE TRIGGER data_sqlite_preparations_fence BEFORE INSERT ON data_sqlite_preparations
WHEN NOT EXISTS (SELECT 1 FROM data_fences f WHERE f.id=NEW.fence_id AND f.database_id=NEW.database_id AND f.state='held')
 OR NOT EXISTS (SELECT 1 FROM data_replica_bindings b WHERE b.id=NEW.binding_id AND b.database_id=NEW.database_id AND b.epoch_id=NEW.epoch_id)
 OR EXISTS (SELECT 1 FROM data_sqlite_preparations p WHERE p.fence_id=NEW.fence_id AND NOT EXISTS (SELECT 1 FROM data_sqlite_point_consumers c WHERE c.preparation_id=p.id))
 OR (NEW.supersedes IS NULL AND EXISTS (SELECT 1 FROM data_fences f WHERE f.id=NEW.fence_id AND f.operation_id<>NEW.operation_id))
 OR (NEW.supersedes IS NOT NULL AND NOT EXISTS (SELECT 1 FROM data_sqlite_point_consumers c JOIN data_sqlite_preparations p ON p.id=c.preparation_id WHERE c.preparation_id=NEW.supersedes AND c.kind='supersede' AND c.successor_id=NEW.id AND c.operation_id=NEW.operation_id AND p.fence_id=NEW.fence_id AND p.database_id=NEW.database_id))
BEGIN SELECT RAISE(ABORT,'unprotected SQLite preparation'); END;

CREATE TABLE data_sqlite_preparation_events(
 preparation_id TEXT NOT NULL REFERENCES data_sqlite_preparations(id),
 seq INTEGER NOT NULL CHECK(seq BETWEEN 1 AND 10),
 stage TEXT NOT NULL CHECK(stage IN ('claimed','writers_stop_intent','writers_stopped','replica_stop_intent','replica_stopped','coverage_intent','coverage_uploaded','coverage_retained','verified','prepared')),
 canonical BLOB NOT NULL CHECK(length(canonical)<=131072),
 PRIMARY KEY(preparation_id,seq));
CREATE TRIGGER data_sqlite_preparation_events_no_update BEFORE UPDATE ON data_sqlite_preparation_events BEGIN SELECT RAISE(ABORT,'append-only SQLite preparation event'); END;
CREATE TRIGGER data_sqlite_preparation_events_no_delete BEFORE DELETE ON data_sqlite_preparation_events BEGIN SELECT RAISE(ABORT,'append-only SQLite preparation event'); END;
CREATE TRIGGER data_sqlite_preparation_events_order BEFORE INSERT ON data_sqlite_preparation_events
WHEN NEW.seq<>COALESCE((SELECT MAX(seq)+1 FROM data_sqlite_preparation_events WHERE preparation_id=NEW.preparation_id),1)
 OR (NEW.seq=1)<>(NEW.stage='claimed')
 OR EXISTS (SELECT 1 FROM data_sqlite_preparation_events WHERE preparation_id=NEW.preparation_id AND stage='prepared')
 OR (NEW.stage='prepared' AND NOT EXISTS (SELECT 1 FROM data_sqlite_prepared_points WHERE preparation_id=NEW.preparation_id))
 OR EXISTS (SELECT 1 FROM data_sqlite_point_consumers WHERE preparation_id=NEW.preparation_id)
BEGIN SELECT RAISE(ABORT,'SQLite preparation event order'); END;

CREATE TABLE data_sqlite_prepared_points(
 preparation_id TEXT PRIMARY KEY REFERENCES data_sqlite_preparations(id),
 source_point_id TEXT NOT NULL REFERENCES data_restore_points(id),
 canonical BLOB NOT NULL CHECK(length(canonical)<=131072));
CREATE TRIGGER data_sqlite_prepared_points_no_update BEFORE UPDATE ON data_sqlite_prepared_points BEGIN SELECT RAISE(ABORT,'immutable SQLite prepared point'); END;
CREATE TRIGGER data_sqlite_prepared_points_no_delete BEFORE DELETE ON data_sqlite_prepared_points BEGIN SELECT RAISE(ABORT,'immutable SQLite prepared point'); END;
CREATE TRIGGER data_sqlite_prepared_points_held BEFORE INSERT ON data_sqlite_prepared_points
WHEN NOT EXISTS (SELECT 1 FROM data_sqlite_preparations p JOIN data_fences f ON f.id=p.fence_id JOIN data_restore_points r ON r.id=NEW.source_point_id WHERE p.id=NEW.preparation_id AND f.state='held' AND r.binding_id=p.binding_id AND r.epoch_id=p.epoch_id)
 OR EXISTS (SELECT 1 FROM data_sqlite_point_consumers WHERE preparation_id=NEW.preparation_id)
BEGIN SELECT RAISE(ABORT,'unprotected SQLite prepared point'); END;

CREATE TABLE data_sqlite_point_consumers(
 preparation_id TEXT PRIMARY KEY REFERENCES data_sqlite_preparations(id),
 operation_id TEXT NOT NULL UNIQUE REFERENCES operations(id),
 kind TEXT NOT NULL CHECK(kind IN ('supersede','abandon','reviewed_mutation')),
 successor_id TEXT,
 canonical BLOB NOT NULL CHECK(length(canonical)<=16384),
 CHECK((kind='supersede')=(successor_id IS NOT NULL)));
CREATE TRIGGER data_sqlite_point_consumers_no_update BEFORE UPDATE ON data_sqlite_point_consumers BEGIN SELECT RAISE(ABORT,'immutable SQLite point consumer'); END;
CREATE TRIGGER data_sqlite_point_consumers_no_delete BEFORE DELETE ON data_sqlite_point_consumers BEGIN SELECT RAISE(ABORT,'immutable SQLite point consumer'); END;
CREATE TRIGGER data_sqlite_point_consumers_held BEFORE INSERT ON data_sqlite_point_consumers
WHEN NOT EXISTS (SELECT 1 FROM data_sqlite_preparations p JOIN data_fences f ON f.id=p.fence_id WHERE p.id=NEW.preparation_id AND f.state='held')
 OR (NEW.kind='reviewed_mutation' AND NOT EXISTS (SELECT 1 FROM data_sqlite_prepared_points WHERE preparation_id=NEW.preparation_id))
BEGIN SELECT RAISE(ABORT,'unprotected SQLite point consumer'); END;

CREATE TRIGGER data_sqlite_point_consumers_identity BEFORE INSERT ON data_sqlite_point_consumers
WHEN NOT EXISTS (
 SELECT 1 FROM operations o
 JOIN data_sqlite_task_inputs i ON i.id=o.secret_ref
 JOIN data_sqlite_preparations p ON p.id=NEW.preparation_id
 JOIN operations previous ON previous.id=p.operation_id
 WHERE o.id=NEW.operation_id AND o.state='preflight' AND i.kind=o.kind AND i.app=o.app AND i.database_id=p.database_id
 AND previous.state IN ('succeeded','failed','recovery_required')
 AND json_extract(NEW.canonical,'$.preparation_id')=NEW.preparation_id
 AND json_extract(NEW.canonical,'$.operation_id')=NEW.operation_id
 AND json_extract(NEW.canonical,'$.kind')=NEW.kind
 AND json_extract(NEW.canonical,'$.app')=o.app AND o.app=json_extract(p.canonical,'$.task.app')
 AND json_extract(NEW.canonical,'$.requester')=o.requester AND o.requester=json_extract(p.canonical,'$.task.requester')
 AND json_extract(NEW.canonical,'$.input_sha256')=o.secret_ref
 AND ((NEW.kind='supersede' AND o.kind='sqlite_prepare' AND i.preparation_id=NEW.successor_id AND i.supersedes=p.id AND json_extract(NEW.canonical,'$.successor_id')=NEW.successor_id)
 OR (NEW.kind='abandon' AND o.kind='sqlite_prepare_abandon' AND i.preparation_id=p.id AND i.supersedes IS NULL)))
BEGIN SELECT RAISE(ABORT,'SQLite consumer identity is not exact'); END;

CREATE TABLE data_sqlite_consumer_completions(
 preparation_id TEXT PRIMARY KEY REFERENCES data_sqlite_point_consumers(preparation_id),
 operation_id TEXT NOT NULL UNIQUE REFERENCES operations(id),
 fence_id TEXT NOT NULL REFERENCES data_fences(id));
CREATE TRIGGER data_sqlite_consumer_completions_no_update BEFORE UPDATE ON data_sqlite_consumer_completions BEGIN SELECT RAISE(ABORT,'immutable SQLite consumer completion'); END;
CREATE TRIGGER data_sqlite_consumer_completions_no_delete BEFORE DELETE ON data_sqlite_consumer_completions BEGIN SELECT RAISE(ABORT,'immutable SQLite consumer completion'); END;
CREATE TRIGGER data_sqlite_consumer_completions_exact BEFORE INSERT ON data_sqlite_consumer_completions
WHEN NOT EXISTS (SELECT 1 FROM data_sqlite_point_consumers c JOIN data_sqlite_preparations p ON p.id=c.preparation_id JOIN operations o ON o.id=c.operation_id JOIN operation_outcomes r ON r.operation_id=o.id
 WHERE c.preparation_id=NEW.preparation_id AND c.operation_id=NEW.operation_id AND c.kind='abandon' AND p.fence_id=NEW.fence_id
 AND o.kind='sqlite_prepare_abandon' AND o.state='succeeded'
 AND json_extract(r.canonical,'$.receipt.preparation_id')=NEW.preparation_id
 AND json_extract(r.canonical,'$.receipt.task.operation_id')=NEW.operation_id
 AND json_extract(r.canonical,'$.receipt.outcome')='abandoned_stopped'
 AND json_extract(r.canonical,'$.receipt.fence.id')=NEW.fence_id
 AND json_extract(r.canonical,'$.receipt.fence.state')='released')
 OR EXISTS (SELECT 1 FROM data_sqlite_preparations p WHERE p.fence_id=NEW.fence_id AND NOT EXISTS (SELECT 1 FROM data_sqlite_point_consumers c WHERE c.preparation_id=p.id))
 OR EXISTS (SELECT 1 FROM data_sqlite_point_consumers c JOIN data_sqlite_preparations p ON p.id=c.preparation_id WHERE p.fence_id=NEW.fence_id AND c.kind='reviewed_mutation' AND c.preparation_id<>NEW.preparation_id AND NOT EXISTS (SELECT 1 FROM data_sqlite_consumer_completions d WHERE d.preparation_id=c.preparation_id))
BEGIN SELECT RAISE(ABORT,'SQLite consumer completion is not exact'); END;

CREATE TRIGGER data_fences_sqlite_preparation_release BEFORE UPDATE OF state ON data_fences
WHEN OLD.state='held' AND NEW.state<>'held' AND EXISTS (SELECT 1 FROM data_sqlite_preparations WHERE fence_id=OLD.id)
 AND NOT EXISTS (SELECT 1 FROM data_sqlite_consumer_completions WHERE fence_id=OLD.id)
BEGIN SELECT RAISE(ABORT,'SQLite preparation protects fence'); END;

CREATE TABLE data_sqlite_task_settlements(
 operation_id TEXT PRIMARY KEY REFERENCES operations(id),
 code TEXT NOT NULL CHECK(code IN ('local_uncertainty','stopped_source_changed','supersede_coverage_unavailable','required_credentials_expired','invalid_evidence','effect_unknown')));
CREATE TRIGGER data_sqlite_task_settlements_no_update BEFORE UPDATE ON data_sqlite_task_settlements BEGIN SELECT RAISE(ABORT,'immutable SQLite task settlement'); END;
CREATE TRIGGER data_sqlite_task_settlements_no_delete BEFORE DELETE ON data_sqlite_task_settlements BEGIN SELECT RAISE(ABORT,'immutable SQLite task settlement'); END;
UPDATE schema_version SET version=35;
`)
	return err
}
