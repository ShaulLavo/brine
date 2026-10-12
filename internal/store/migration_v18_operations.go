package store

import (
	"context"
	"database/sql"

	"github.com/ShaulLavo/brine/internal/ops"
)

func migrateModulePublicationOperations(ctx context.Context, tx *sql.Tx) error {
	const migration = `
CREATE TABLE operations_v18 (
 id TEXT PRIMARY KEY, plan_id TEXT REFERENCES plans(id), requester TEXT NOT NULL, idempotency_key TEXT NOT NULL,
 state TEXT NOT NULL CHECK(state IN ('queued','launch_unknown','preflight','preparing','quiescing','starting','checking','committing','rolling_back','succeeded','failed','rolled_back','recovery_required')),
 created_at TEXT NOT NULL, updated_at TEXT NOT NULL,
 kind TEXT NOT NULL CHECK(kind IN ('deploy','secret_set','reconcile','resolve','restore_test','credential_activation','data_init_apply','native_capture','native_restore_test','native_prepare','module_publish_apply','module_publish_resolve')), app TEXT NOT NULL, secret_ref TEXT NOT NULL,
 recovery_of TEXT NOT NULL DEFAULT '',
 CHECK((kind IN ('restore_test','credential_activation','data_init_apply','native_capture','native_restore_test','native_prepare','module_publish_apply') AND plan_id IS NULL AND app<>'' AND secret_ref<>'' AND recovery_of='' AND state IN ('queued','launch_unknown','preflight','succeeded','failed','recovery_required')) OR (kind='module_publish_resolve' AND plan_id IS NULL AND app<>'' AND secret_ref<>'' AND recovery_of<>'' AND state IN ('queued','launch_unknown','preflight','succeeded','failed','recovery_required')) OR (kind='deploy' AND plan_id IS NOT NULL AND secret_ref='' AND recovery_of='') OR (kind='resolve' AND recovery_of<>'' AND ((plan_id IS NOT NULL AND secret_ref='') OR (plan_id IS NULL AND secret_ref<>''))) OR (kind='secret_set' AND plan_id IS NULL AND secret_ref<>'' AND recovery_of='' AND state IN ('queued','preparing','succeeded','failed','recovery_required')) OR (kind='reconcile' AND plan_id IS NULL AND app='' AND secret_ref='' AND recovery_of='' AND state IN ('queued','launch_unknown','preflight','succeeded','failed','recovery_required'))),
 UNIQUE(requester,idempotency_key));
INSERT INTO operations_v18 SELECT * FROM operations;
DROP TABLE operations;
ALTER TABLE operations_v18 RENAME TO operations;
CREATE TRIGGER legal_transition BEFORE UPDATE OF state ON operations WHEN OLD.state<>NEW.state AND NOT EXISTS (SELECT 1 FROM transitions WHERE kind=OLD.kind AND from_state=OLD.state AND to_state=NEW.state) BEGIN SELECT RAISE(ABORT,'illegal state transition'); END;
CREATE TRIGGER operation_identity BEFORE UPDATE OF id,plan_id,requester,idempotency_key,kind,app,secret_ref,recovery_of ON operations BEGIN SELECT RAISE(ABORT,'immutable operation identity'); END;
CREATE UNIQUE INDEX unresolved_source ON operations(recovery_of) WHERE kind='resolve' AND state NOT IN ('failed','recovery_required');
`
	if _, err := tx.ExecContext(ctx, migration); err != nil {
		return err
	}
	for _, kind := range []ops.Kind{ops.ModulePublishApply, ops.ModulePublishResolve} {
		if err := insertNativeTaskTransitions(ctx, tx, kind); err != nil {
			return err
		}
	}
	return nil
}
