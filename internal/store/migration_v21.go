package store

import (
	"context"
	"database/sql"
)

func migrateNativeArchive(ctx context.Context, tx *sql.Tx) error {
	_, err := tx.ExecContext(ctx, `
CREATE TABLE data_native_archive_verifications(archive_id TEXT NOT NULL REFERENCES data_archive_plans(archive_id),point_id TEXT NOT NULL REFERENCES data_native_prepared_points(id),canonical BLOB NOT NULL CHECK(length(canonical)<=65536),PRIMARY KEY(archive_id,point_id));
CREATE TRIGGER data_native_archive_verifications_no_update BEFORE UPDATE ON data_native_archive_verifications BEGIN SELECT RAISE(ABORT,'immutable native archive replay'); END;
CREATE TRIGGER data_native_archive_verifications_no_delete BEFORE DELETE ON data_native_archive_verifications BEGIN SELECT RAISE(ABORT,'retained native archive replay'); END;
CREATE TABLE data_native_archive_replays(id TEXT PRIMARY KEY,archive_id TEXT NOT NULL REFERENCES data_archive_plans(archive_id),canonical BLOB NOT NULL CHECK(length(canonical)<=65536));
CREATE TRIGGER data_native_archive_replays_no_update BEFORE UPDATE ON data_native_archive_replays BEGIN SELECT RAISE(ABORT,'immutable native archive replay'); END;
CREATE TRIGGER data_native_archive_replays_no_delete BEFORE DELETE ON data_native_archive_replays BEGIN SELECT RAISE(ABORT,'retained native archive replay'); END;
CREATE TABLE data_native_archive_claims(
 archive_id TEXT NOT NULL REFERENCES data_archive_plans(archive_id),
 archive_operation_id TEXT NOT NULL REFERENCES data_archive_operations(id),
 task_operation_id TEXT NOT NULL UNIQUE REFERENCES operations(id),
 binding_id TEXT NOT NULL REFERENCES data_replica_bindings(id),
 point_id TEXT NOT NULL UNIQUE REFERENCES data_native_prepared_points(id),
 original_fence_owner TEXT NOT NULL,
 canonical BLOB NOT NULL CHECK(length(canonical)<=4096),
 PRIMARY KEY(archive_id,binding_id)
);
CREATE TRIGGER data_native_archive_claims_no_update BEFORE UPDATE ON data_native_archive_claims BEGIN SELECT RAISE(ABORT,'immutable native retirement consumer'); END;
CREATE TRIGGER data_native_archive_claims_no_delete BEFORE DELETE ON data_native_archive_claims BEGIN SELECT RAISE(ABORT,'retained native retirement consumer'); END;
CREATE TABLE data_native_archive_trees(
 archive_id TEXT NOT NULL REFERENCES data_archive_plans(archive_id),
 binding_id TEXT NOT NULL REFERENCES data_replica_bindings(id),
 transition_id TEXT NOT NULL REFERENCES data_engine_transition_inputs(id),
 canonical BLOB NOT NULL CHECK(length(canonical)<=65536),
 PRIMARY KEY(archive_id,transition_id)
);
CREATE TABLE data_native_archive_dependencies(
 archive_id TEXT NOT NULL REFERENCES data_archive_plans(archive_id),
 binding_id TEXT NOT NULL REFERENCES data_replica_bindings(id),
 epoch_id TEXT NOT NULL,
 dependency_id TEXT NOT NULL,
 canonical BLOB NOT NULL CHECK(length(canonical)<=65536),
 PRIMARY KEY(archive_id,binding_id,epoch_id,dependency_id)
);
CREATE TABLE data_native_archive_tree_events(
 archive_id TEXT NOT NULL REFERENCES data_archive_plans(archive_id),
 sequence INTEGER NOT NULL CHECK(sequence>0),
 transition_id TEXT NOT NULL,
 stage TEXT NOT NULL CHECK(stage IN ('intent','moved')),
 canonical BLOB NOT NULL CHECK(length(canonical)<=65536),
 PRIMARY KEY(archive_id,sequence),
 FOREIGN KEY(archive_id,transition_id) REFERENCES data_native_archive_trees(archive_id,transition_id)
);
CREATE TRIGGER data_native_archive_trees_no_update BEFORE UPDATE ON data_native_archive_trees BEGIN SELECT RAISE(ABORT,'immutable native archive closure'); END;
CREATE TRIGGER data_native_archive_trees_no_delete BEFORE DELETE ON data_native_archive_trees BEGIN SELECT RAISE(ABORT,'immutable native archive closure'); END;
CREATE TRIGGER data_native_archive_dependencies_no_update BEFORE UPDATE ON data_native_archive_dependencies BEGIN SELECT RAISE(ABORT,'immutable native archive dependencies'); END;
CREATE TRIGGER data_native_archive_dependencies_no_delete BEFORE DELETE ON data_native_archive_dependencies BEGIN SELECT RAISE(ABORT,'immutable native archive dependencies'); END;
CREATE TRIGGER data_native_archive_tree_events_no_update BEFORE UPDATE ON data_native_archive_tree_events BEGIN SELECT RAISE(ABORT,'append-only native archive movement'); END;
CREATE TRIGGER data_native_archive_tree_events_no_delete BEFORE DELETE ON data_native_archive_tree_events BEGIN SELECT RAISE(ABORT,'append-only native archive movement'); END;
UPDATE schema_version SET version=21;
`)
	return err
}
