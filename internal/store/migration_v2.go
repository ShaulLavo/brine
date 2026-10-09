package store

import (
	"context"
	"database/sql"

	"github.com/ShaulLavo/brine/internal/ops"
)

// Rebuild both sides of the foreign key together, keeping event sequence and
// payload bytes unchanged. The complete migration commits atomically.
func migrateOperations(ctx context.Context, tx *sql.Tx) error {
	const migration = `
CREATE TABLE operations_v2 (
 id TEXT PRIMARY KEY, plan_id TEXT REFERENCES plans(id), requester TEXT NOT NULL, idempotency_key TEXT NOT NULL,
 state TEXT NOT NULL CHECK(state IN ('queued','launch_unknown','preflight','preparing','quiescing','starting','checking','committing','rolling_back','succeeded','failed','rolled_back','recovery_required')),
 created_at TEXT NOT NULL, updated_at TEXT NOT NULL,
 kind TEXT NOT NULL CHECK(kind IN ('deploy','secret_set')), app TEXT NOT NULL, secret_ref TEXT NOT NULL,
 CHECK((kind='deploy' AND plan_id IS NOT NULL AND secret_ref='') OR (kind='secret_set' AND plan_id IS NULL AND secret_ref<>'' AND state IN ('queued','preparing','succeeded','failed','recovery_required'))),
 UNIQUE(requester,idempotency_key));
INSERT INTO operations_v2 SELECT o.*, 'deploy', json_extract(p.canonical,'$.app'), '' FROM operations o LEFT JOIN plans p ON p.id=o.plan_id;
CREATE TABLE events_v2 (operation_id TEXT NOT NULL REFERENCES operations_v2(id), seq INTEGER NOT NULL CHECK(seq>0), kind TEXT NOT NULL, state TEXT NOT NULL, payload BLOB NOT NULL CHECK(length(payload)<=4096), created_at TEXT NOT NULL, PRIMARY KEY(operation_id,seq));
INSERT INTO events_v2 SELECT * FROM events;
DROP TABLE events;
DROP TABLE operations;
ALTER TABLE operations_v2 RENAME TO operations;
ALTER TABLE events_v2 RENAME TO events;
DROP TABLE transitions;
CREATE TABLE transitions (kind TEXT NOT NULL, from_state TEXT NOT NULL, to_state TEXT NOT NULL, PRIMARY KEY(kind,from_state,to_state));
CREATE TRIGGER legal_transition BEFORE UPDATE OF state ON operations WHEN OLD.state<>NEW.state AND NOT EXISTS (SELECT 1 FROM transitions WHERE kind=OLD.kind AND from_state=OLD.state AND to_state=NEW.state) BEGIN SELECT RAISE(ABORT,'illegal state transition'); END;
CREATE TRIGGER operation_identity BEFORE UPDATE OF id,plan_id,requester,idempotency_key,kind,app,secret_ref ON operations BEGIN SELECT RAISE(ABORT,'immutable operation identity'); END;
CREATE TRIGGER events_no_update BEFORE UPDATE ON events BEGIN SELECT RAISE(ABORT,'append-only events'); END;
CREATE TRIGGER events_no_delete BEFORE DELETE ON events BEGIN SELECT RAISE(ABORT,'append-only events'); END;
CREATE TRIGGER events_order BEFORE INSERT ON events WHEN NEW.seq<>COALESCE((SELECT MAX(seq)+1 FROM events WHERE operation_id=NEW.operation_id),1) BEGIN SELECT RAISE(ABORT,'event sequence'); END;
UPDATE schema_version SET version=2;
`
	if _, err := tx.ExecContext(ctx, migration); err != nil {
		return err
	}
	for _, kind := range []ops.Kind{ops.Deploy, ops.SecretSet} {
		for from, tos := range ops.TransitionsFor(kind) {
			for _, to := range tos {
				if _, err := tx.ExecContext(ctx, "INSERT INTO transitions VALUES(?,?,?)", kind, from, to); err != nil {
					return err
				}
			}
		}
	}
	return nil
}
