package store

import (
	"context"
	"database/sql"
)

// Review payloads are retained with plans. Digest-addressed artifacts are
// published separately, only after the fenced apply intent has been claimed.
func migrateInitializationArtifacts(ctx context.Context, tx *sql.Tx) error {
	_, err := tx.ExecContext(ctx, `
 CREATE TABLE data_init_reviews(plan_id TEXT PRIMARY KEY REFERENCES data_init_plans(id),artifact BLOB NOT NULL CHECK(length(artifact)<=1048576),catalog BLOB NOT NULL CHECK(length(catalog)<=2097152));
 CREATE TRIGGER data_init_reviews_no_update BEFORE UPDATE ON data_init_reviews BEGIN SELECT RAISE(ABORT,'immutable initialization review'); END;
 CREATE TRIGGER data_init_reviews_no_delete BEFORE DELETE ON data_init_reviews BEGIN SELECT RAISE(ABORT,'immutable initialization review'); END;
 CREATE TABLE data_init_artifacts(digest TEXT PRIMARY KEY,bytes BLOB NOT NULL CHECK(length(bytes)<=1048576));
 CREATE TRIGGER data_init_artifacts_no_update BEFORE UPDATE ON data_init_artifacts BEGIN SELECT RAISE(ABORT,'immutable initializer'); END;
 CREATE TRIGGER data_init_artifacts_no_delete BEFORE DELETE ON data_init_artifacts BEGIN SELECT RAISE(ABORT,'immutable initializer'); END;
 UPDATE schema_version SET version=11;
 `)
	return err
}
