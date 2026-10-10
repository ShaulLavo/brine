package store

import (
	"context"
	"database/sql"
)

func migrateTasks(ctx context.Context, tx *sql.Tx) error {
	const migration = `
CREATE TABLE operations_v7 (
 id TEXT PRIMARY KEY, plan_id TEXT REFERENCES plans(id), requester TEXT NOT NULL, idempotency_key TEXT NOT NULL,
 state TEXT NOT NULL CHECK(state IN ('queued','launch_unknown','preflight','preparing','quiescing','starting','checking','committing','rolling_back','succeeded','failed','rolled_back','recovery_required')),
 created_at TEXT NOT NULL, updated_at TEXT NOT NULL,
 kind TEXT NOT NULL CHECK(kind IN ('deploy','secret_set','reconcile','resolve','restore_test','credential_activation','data_init_apply')), app TEXT NOT NULL, secret_ref TEXT NOT NULL,
 recovery_of TEXT NOT NULL DEFAULT '',
 CHECK((kind IN ('restore_test','credential_activation','data_init_apply') AND plan_id IS NULL AND app<>'' AND secret_ref<>'' AND recovery_of='' AND state IN ('queued','launch_unknown','preflight','succeeded','failed','recovery_required')) OR (kind='deploy' AND plan_id IS NOT NULL AND secret_ref='' AND recovery_of='') OR (kind='resolve' AND recovery_of<>'' AND ((plan_id IS NOT NULL AND secret_ref='') OR (plan_id IS NULL AND secret_ref<>''))) OR (kind='secret_set' AND plan_id IS NULL AND secret_ref<>'' AND recovery_of='' AND state IN ('queued','preparing','succeeded','failed','recovery_required')) OR (kind='reconcile' AND plan_id IS NULL AND app='' AND secret_ref='' AND recovery_of='' AND state IN ('queued','launch_unknown','preflight','succeeded','failed','recovery_required'))),
 UNIQUE(requester,idempotency_key));
INSERT INTO operations_v7 SELECT * FROM operations;
DROP TABLE operations;
ALTER TABLE operations_v7 RENAME TO operations;
CREATE TRIGGER legal_transition BEFORE UPDATE OF state ON operations WHEN OLD.state<>NEW.state AND NOT EXISTS (SELECT 1 FROM transitions WHERE kind=OLD.kind AND from_state=OLD.state AND to_state=NEW.state) BEGIN SELECT RAISE(ABORT,'illegal state transition'); END;
CREATE TRIGGER operation_identity BEFORE UPDATE OF id,plan_id,requester,idempotency_key,kind,app,secret_ref,recovery_of ON operations BEGIN SELECT RAISE(ABORT,'immutable operation identity'); END;
CREATE UNIQUE INDEX unresolved_source ON operations(recovery_of) WHERE kind='resolve' AND state NOT IN ('failed','recovery_required');
CREATE TABLE operation_outcomes(operation_id TEXT PRIMARY KEY REFERENCES operations(id),canonical BLOB NOT NULL CHECK(length(canonical)<=65536));
CREATE TRIGGER operation_outcomes_immutable BEFORE UPDATE ON operation_outcomes BEGIN SELECT RAISE(ABORT,'immutable task outcome'); END;
CREATE TRIGGER operation_outcomes_no_delete BEFORE DELETE ON operation_outcomes BEGIN SELECT RAISE(ABORT,'durable task outcome'); END;
CREATE TABLE data_credential_rotations_v7(plan_id TEXT PRIMARY KEY,binding_id TEXT NOT NULL REFERENCES data_replica_bindings(id),stage TEXT NOT NULL CHECK(stage IN ('prepared','stop_issued','stopped','committed','start_issued','active','verified','superseded')),canonical BLOB NOT NULL CHECK(length(canonical)<=32768));
INSERT INTO data_credential_rotations_v7 SELECT * FROM data_credential_rotations;
DROP TABLE data_credential_rotations;
ALTER TABLE data_credential_rotations_v7 RENAME TO data_credential_rotations;
CREATE UNIQUE INDEX data_one_pending_rotation ON data_credential_rotations(binding_id) WHERE stage NOT IN ('active','verified','superseded');
CREATE TRIGGER data_rotation_identity BEFORE UPDATE OF plan_id,binding_id ON data_credential_rotations BEGIN SELECT RAISE(ABORT,'immutable rotation identity'); END;
CREATE TRIGGER data_rotation_no_delete BEFORE DELETE ON data_credential_rotations BEGIN SELECT RAISE(ABORT,'durable credential rotation'); END;
CREATE TABLE restore_task_inputs(id TEXT PRIMARY KEY,requester TEXT NOT NULL,canonical BLOB NOT NULL CHECK(length(canonical)<=4096));
CREATE TRIGGER restore_task_inputs_no_update BEFORE UPDATE ON restore_task_inputs BEGIN SELECT RAISE(ABORT,'immutable restore request'); END;
CREATE TRIGGER restore_task_inputs_no_delete BEFORE DELETE ON restore_task_inputs BEGIN SELECT RAISE(ABORT,'durable restore request'); END;
UPDATE schema_version SET version=7;
`
	if _, err := tx.ExecContext(ctx, migration); err != nil {
		return err
	}
	return nil
}
