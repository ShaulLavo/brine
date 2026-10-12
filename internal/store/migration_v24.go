package store

import (
	"context"
	"database/sql"
)

func migratePublicationReviewAcknowledgements(ctx context.Context, tx *sql.Tx) error {
	_, err := tx.ExecContext(ctx, `CREATE TABLE data_module_publication_approvals(
 plan_id TEXT PRIMARY KEY REFERENCES data_module_publication_reviews(plan_id),
 requester TEXT NOT NULL, point_id TEXT NOT NULL REFERENCES data_native_prepared_points(id),
 target_declaration_id TEXT NOT NULL REFERENCES plans(id),
 record_sha256 TEXT NOT NULL CHECK(length(record_sha256)=64),
 canonical BLOB NOT NULL CHECK(length(canonical)<=4194304));
 CREATE TRIGGER data_module_publication_approvals_no_update BEFORE UPDATE ON data_module_publication_approvals BEGIN SELECT RAISE(ABORT,'immutable publication approval'); END;
 CREATE TRIGGER data_module_publication_approvals_no_delete BEFORE DELETE ON data_module_publication_approvals BEGIN SELECT RAISE(ABORT,'retained publication approval'); END;
 CREATE TABLE data_module_publication_effects(
 nonce TEXT PRIMARY KEY, plan_id TEXT NOT NULL REFERENCES data_module_publication_claims(plan_id),
 operation_id TEXT NOT NULL REFERENCES operations(id), step TEXT NOT NULL,
 canonical BLOB NOT NULL CHECK(length(canonical)<=65536));
 CREATE TRIGGER data_module_publication_effects_no_update BEFORE UPDATE ON data_module_publication_effects BEGIN SELECT RAISE(ABORT,'immutable publication effect'); END;
 CREATE TRIGGER data_module_publication_effects_no_delete BEFORE DELETE ON data_module_publication_effects BEGIN SELECT RAISE(ABORT,'retained publication effect'); END;
 CREATE TABLE data_module_publication_effect_events(
 nonce TEXT NOT NULL REFERENCES data_module_publication_effects(nonce), sequence INTEGER NOT NULL CHECK(sequence>0),
 stage TEXT NOT NULL CHECK(stage IN ('started','stop_intent','disposed','unknown')),
 canonical BLOB NOT NULL CHECK(length(canonical)<=65536), PRIMARY KEY(nonce,sequence), UNIQUE(nonce,stage));
 CREATE TRIGGER data_module_publication_effect_events_no_update BEFORE UPDATE ON data_module_publication_effect_events BEGIN SELECT RAISE(ABORT,'immutable publication effect event'); END;
 CREATE TRIGGER data_module_publication_effect_events_no_delete BEFORE DELETE ON data_module_publication_effect_events BEGIN SELECT RAISE(ABORT,'retained publication effect event'); END;
 CREATE TABLE data_module_publication_target_intents(
 plan_id TEXT PRIMARY KEY REFERENCES data_module_publication_claims(plan_id),
 transition_id TEXT NOT NULL UNIQUE REFERENCES data_native_repository_intents(id),
 record_sha256 TEXT NOT NULL CHECK(length(record_sha256)=64),
 canonical BLOB NOT NULL CHECK(length(canonical)<=2097152));
 CREATE TRIGGER data_module_publication_target_intents_no_update BEFORE UPDATE ON data_module_publication_target_intents BEGIN SELECT RAISE(ABORT,'immutable publication target intent'); END;
 CREATE TRIGGER data_module_publication_target_intents_no_delete BEFORE DELETE ON data_module_publication_target_intents BEGIN SELECT RAISE(ABORT,'retained publication target intent'); END;
 UPDATE schema_version SET version=24;`)
	return err
}
