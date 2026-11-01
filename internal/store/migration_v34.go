package store

import (
	"context"
	"database/sql"
)

func migrateUDPReservations(ctx context.Context, tx *sql.Tx) error {
	_, err := tx.ExecContext(ctx, `
CREATE TABLE release_heads_v34 (
 app TEXT PRIMARY KEY, current_id TEXT NOT NULL, previous_id TEXT,
 incarnation_id TEXT NOT NULL REFERENCES plans(id),
 FOREIGN KEY(app,current_id) REFERENCES releases(app,id),
 FOREIGN KEY(app,previous_id) REFERENCES releases(app,id));
INSERT INTO release_heads_v34 SELECT h.app,h.current_id,h.previous_id,r.plan_id FROM release_heads h JOIN releases r ON r.app=h.app AND r.id=h.current_id;
DROP TABLE release_heads;
ALTER TABLE release_heads_v34 RENAME TO release_heads;
CREATE TRIGGER release_head_incarnation BEFORE UPDATE OF incarnation_id ON release_heads BEGIN SELECT RAISE(ABORT,'immutable app incarnation'); END;
CREATE TABLE udp_reservations (
 id TEXT PRIMARY KEY, app TEXT NOT NULL, incarnation TEXT NOT NULL,
 family TEXT NOT NULL CHECK(family IN ('ipv4','ipv6')), bind_address TEXT NOT NULL,
 host_port INTEGER NOT NULL CHECK(host_port BETWEEN 1024 AND 65535),
 state TEXT NOT NULL CHECK(state IN ('staged','active','releasing','released')),
 owner TEXT REFERENCES operations(id), created_by TEXT NOT NULL REFERENCES operations(id),
 released_by TEXT REFERENCES operations(id), canonical BLOB NOT NULL CHECK(length(canonical)<=4096),
 CHECK((state IN ('staged','releasing')) = (owner IS NOT NULL)),
 CHECK((state='released') = (released_by IS NOT NULL)));
CREATE UNIQUE INDEX udp_live_endpoint ON udp_reservations(bind_address,host_port) WHERE state<>'released';
CREATE UNIQUE INDEX udp_one_claim ON udp_reservations(app) WHERE state='staged';
CREATE UNIQUE INDEX udp_one_holder ON udp_reservations(app) WHERE state IN ('active','releasing');
CREATE TRIGGER udp_reservations_no_delete BEFORE DELETE ON udp_reservations BEGIN SELECT RAISE(ABORT,'retained reservation'); END;
CREATE TRIGGER udp_reservation_identity BEFORE UPDATE OF id,app,incarnation,family,bind_address,host_port,created_by,canonical ON udp_reservations BEGIN SELECT RAISE(ABORT,'immutable reservation'); END;
CREATE TRIGGER udp_legal_transition BEFORE UPDATE OF state ON udp_reservations WHEN OLD.state<>NEW.state AND NOT (
 (OLD.state='staged' AND NEW.state IN ('active','released')) OR
 (OLD.state='active' AND NEW.state='releasing') OR
 (OLD.state='releasing' AND NEW.state IN ('active','released')))
 BEGIN SELECT RAISE(ABORT,'illegal UDP reservation transition'); END;
UPDATE schema_version SET version=34;
`)
	return err
}
