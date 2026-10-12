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
 UPDATE schema_version SET version=24;`)
	return err
}
