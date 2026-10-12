package store

import (
	"context"
	"database/sql"
)

func migrateModulePublicationRecords(ctx context.Context, tx *sql.Tx) error {
	if err := migrateModulePublicationOperations(ctx, tx); err != nil {
		return err
	}
	const migration = `
CREATE TABLE data_module_publication_reviews(
 plan_id TEXT PRIMARY KEY, point_id TEXT NOT NULL REFERENCES data_native_prepared_points(id),
 binding_id TEXT NOT NULL REFERENCES data_replica_bindings(id), requester TEXT NOT NULL,
 generation INTEGER NOT NULL CHECK(generation>=0), generation_id TEXT NOT NULL,
 point_sha256 TEXT NOT NULL CHECK(length(point_sha256)=64), runtime_version INTEGER NOT NULL CHECK(runtime_version>0),
 runtime_sha256 TEXT NOT NULL CHECK(length(runtime_sha256)=64), storage_sha256 TEXT NOT NULL CHECK(length(storage_sha256)=64),
 canonical BLOB NOT NULL CHECK(length(canonical)<=3145728));
CREATE TRIGGER data_module_publication_reviews_no_update BEFORE UPDATE ON data_module_publication_reviews BEGIN SELECT RAISE(ABORT,'immutable module publication review'); END;
CREATE TRIGGER data_module_publication_reviews_no_delete BEFORE DELETE ON data_module_publication_reviews BEGIN SELECT RAISE(ABORT,'retained module publication review'); END;
CREATE TABLE data_module_publication_claims(
 plan_id TEXT PRIMARY KEY REFERENCES data_module_publication_reviews(plan_id), point_id TEXT NOT NULL UNIQUE REFERENCES data_native_prepared_points(id),
 apply_operation_id TEXT NOT NULL UNIQUE REFERENCES operations(id), requester TEXT NOT NULL,
 binding_id TEXT NOT NULL REFERENCES data_replica_bindings(id), old_epoch_id TEXT NOT NULL, new_epoch_id TEXT NOT NULL,
 consumer_sha256 TEXT NOT NULL CHECK(length(consumer_sha256)=64),
 repository_intent_id TEXT NOT NULL REFERENCES data_native_repository_intents(id),
 repository_intent_sha256 TEXT NOT NULL CHECK(length(repository_intent_sha256)=64),
 canonical BLOB NOT NULL CHECK(length(canonical)<=8192));
CREATE TRIGGER data_module_publication_claims_no_update BEFORE UPDATE ON data_module_publication_claims BEGIN SELECT RAISE(ABORT,'one module publication consumer'); END;
CREATE TRIGGER data_module_publication_claims_no_delete BEFORE DELETE ON data_module_publication_claims BEGIN SELECT RAISE(ABORT,'retained module publication consumer'); END;
CREATE TABLE data_module_publication_cursors(
 plan_id TEXT PRIMARY KEY REFERENCES data_module_publication_claims(plan_id), revision INTEGER NOT NULL CHECK(revision>=0),
 record_sha256 TEXT NOT NULL CHECK(length(record_sha256)=64), canonical BLOB NOT NULL CHECK(length(canonical)<=65536));
CREATE TRIGGER data_module_publication_cursors_cas BEFORE UPDATE ON data_module_publication_cursors WHEN NEW.plan_id<>OLD.plan_id OR NEW.revision<>OLD.revision+1 BEGIN SELECT RAISE(ABORT,'module publication cursor sequence'); END;
CREATE TRIGGER data_module_publication_cursors_no_delete BEFORE DELETE ON data_module_publication_cursors BEGIN SELECT RAISE(ABORT,'retained module publication cursor'); END;
CREATE TABLE data_module_publication_cursor_records(
 plan_id TEXT NOT NULL REFERENCES data_module_publication_claims(plan_id), revision INTEGER NOT NULL CHECK(revision>=0),
 previous_sha256 TEXT NOT NULL, record_sha256 TEXT NOT NULL CHECK(length(record_sha256)=64),
 canonical BLOB NOT NULL CHECK(length(canonical)<=65536), PRIMARY KEY(plan_id,revision));
CREATE TRIGGER data_module_publication_cursor_records_order BEFORE INSERT ON data_module_publication_cursor_records WHEN NEW.revision<>COALESCE((SELECT MAX(revision)+1 FROM data_module_publication_cursor_records WHERE plan_id=NEW.plan_id),0) BEGIN SELECT RAISE(ABORT,'module publication record sequence'); END;
CREATE TRIGGER data_module_publication_cursor_records_no_update BEFORE UPDATE ON data_module_publication_cursor_records BEGIN SELECT RAISE(ABORT,'append-only module publication record'); END;
CREATE TRIGGER data_module_publication_cursor_records_no_delete BEFORE DELETE ON data_module_publication_cursor_records BEGIN SELECT RAISE(ABORT,'retained module publication record'); END;
CREATE TABLE data_module_publication_proofs(
 plan_id TEXT NOT NULL REFERENCES data_module_publication_claims(plan_id), id TEXT NOT NULL,
 kind TEXT NOT NULL CHECK(kind IN ('descriptor','staging_restore','preflight','publication_receipt','target_verification','target_checkpoint','target_material','recovery_release','retained_dependencies','staging_disposal','completion','guest_review','schema_requirements')),
 evidence_sha256 TEXT NOT NULL CHECK(length(evidence_sha256)=64), canonical BLOB NOT NULL CHECK(length(canonical)<=4194304),
 PRIMARY KEY(plan_id,id));
CREATE TRIGGER data_module_publication_proofs_no_update BEFORE UPDATE ON data_module_publication_proofs BEGIN SELECT RAISE(ABORT,'immutable module publication proof'); END;
CREATE TRIGGER data_module_publication_proofs_no_delete BEFORE DELETE ON data_module_publication_proofs BEGIN SELECT RAISE(ABORT,'retained module publication proof'); END;
CREATE TABLE data_module_publication_resolutions(
 operation_id TEXT PRIMARY KEY REFERENCES operations(id), plan_id TEXT NOT NULL UNIQUE REFERENCES data_module_publication_claims(plan_id),
 original_outcome_sha256 TEXT NOT NULL CHECK(length(original_outcome_sha256)=64),
 canonical BLOB NOT NULL CHECK(length(canonical)<=65536));
CREATE TRIGGER data_module_publication_resolutions_no_update BEFORE UPDATE ON data_module_publication_resolutions BEGIN SELECT RAISE(ABORT,'immutable module publication resolution'); END;
CREATE TRIGGER data_module_publication_resolutions_no_delete BEFORE DELETE ON data_module_publication_resolutions BEGIN SELECT RAISE(ABORT,'retained module publication resolution'); END;
UPDATE schema_version SET version=18;
`
	_, err := tx.ExecContext(ctx, migration)
	return err
}
