package store

import (
	"context"
	"database/sql"
)

func migrateManagedPreparationPortRetirement(ctx context.Context, tx *sql.Tx) error {
	_, err := tx.ExecContext(ctx, `
CREATE TABLE data_engine_preparations_v32(
 binding_id TEXT PRIMARY KEY REFERENCES data_replica_bindings(id),
 plan_id TEXT NOT NULL REFERENCES plans(id),
 operation_id TEXT NOT NULL REFERENCES operations(id),
 engine_port INTEGER NOT NULL CHECK(engine_port BETWEEN 1024 AND 65535),
 frontend_port INTEGER,
 canonical BLOB NOT NULL CHECK(length(canonical)<=4096)
);
INSERT INTO data_engine_preparations_v32 SELECT * FROM data_engine_preparations;
DROP TABLE data_engine_preparations;
ALTER TABLE data_engine_preparations_v32 RENAME TO data_engine_preparations;
CREATE INDEX data_engine_preparations_engine ON data_engine_preparations(engine_port);
CREATE INDEX data_engine_preparations_frontend ON data_engine_preparations(frontend_port) WHERE frontend_port IS NOT NULL;
CREATE TRIGGER data_engine_preparations_active_ports BEFORE INSERT ON data_engine_preparations
WHEN EXISTS (
 SELECT 1 FROM data_engine_preparations p
 JOIN data_replica_bindings b ON b.id=p.binding_id
 JOIN data_databases d ON d.id=b.database_id
 JOIN data_active_incarnations a ON a.incarnation_id=d.incarnation_id
 WHERE p.engine_port=NEW.engine_port OR p.frontend_port=NEW.engine_port
 OR (NEW.frontend_port IS NOT NULL AND (p.engine_port=NEW.frontend_port OR p.frontend_port=NEW.frontend_port))
)
BEGIN SELECT RAISE(ABORT,'active managed port reservation'); END;
CREATE TRIGGER data_engine_preparations_no_update BEFORE UPDATE ON data_engine_preparations BEGIN SELECT RAISE(ABORT,'immutable engine preparation'); END;
CREATE TRIGGER data_engine_preparations_no_delete BEFORE DELETE ON data_engine_preparations BEGIN SELECT RAISE(ABORT,'immutable engine preparation'); END;
UPDATE schema_version SET version=32;
`)
	return err
}
