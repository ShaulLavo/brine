package store

import (
	"context"
	"database/sql"
)

func migratePublicationTransitionProofs(ctx context.Context, tx *sql.Tx) error {
	_, err := tx.ExecContext(ctx, `
CREATE TABLE data_module_publication_proofs_v28(
 plan_id TEXT NOT NULL REFERENCES data_module_publication_claims(plan_id), id TEXT NOT NULL,
 kind TEXT NOT NULL CHECK(kind IN (`+publicationProofKindConstraint()+`)),
 evidence_sha256 TEXT NOT NULL CHECK(length(evidence_sha256)=64), canonical BLOB NOT NULL CHECK(length(canonical)<=4194304),
 PRIMARY KEY(plan_id,id));
INSERT INTO data_module_publication_proofs_v28 SELECT * FROM data_module_publication_proofs;
DROP TABLE data_module_publication_proofs;
ALTER TABLE data_module_publication_proofs_v28 RENAME TO data_module_publication_proofs;
CREATE TRIGGER data_module_publication_proofs_no_update BEFORE UPDATE ON data_module_publication_proofs BEGIN SELECT RAISE(ABORT,'immutable module publication proof'); END;
CREATE TRIGGER data_module_publication_proofs_no_delete BEFORE DELETE ON data_module_publication_proofs BEGIN SELECT RAISE(ABORT,'retained module publication proof'); END;
UPDATE schema_version SET version=28;
`)
	return err
}
