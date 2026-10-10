// Package store owns the target runner's control database, separate from app data.
// SavePlan and LoadPlan verify the exact normalized desired hash and a digest of
// canonical plan bytes. A plan's fingerprint also binds inventory and prior
// releases. Apply must acquire the host lock, collect live inventory and policy,
// load BrineState, and replan-and-compare before any host mutation. Persistence
// alone is neither freshness nor authorization. No transaction does host I/O.
package store

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"path/filepath"
	"regexp"

	"github.com/ShaulLavo/brine/internal/ops"
	"github.com/ShaulLavo/brine/internal/plan"
	"github.com/ShaulLavo/brine/internal/policy"
	_ "modernc.org/sqlite"
)

type PlanID = string
type OpID = string
type Operation = ops.Operation
type Event = ops.Event
type State = ops.State
type Release = ops.Release

const MaxEventBytes = ops.MaxEventBytes
const MaxPlanBytes = 16 << 20
const SchemaVersion = 7

var ErrNotFound = errors.New("control record not found")
var ErrConflict = errors.New("conflicting control record")
var ErrInvalid = errors.New("invalid control record")

type IntegrityError struct{}

func (*IntegrityError) Error() string { return "control record integrity check failed" }

type SchemaError struct{ Version int }

func (e *SchemaError) Error() string {
	return fmt.Sprintf("unsupported control schema version %d", e.Version)
}

type TransitionError struct{ From, To State }

func (*TransitionError) Error() string { return "illegal operation state transition" }

type StateConflictError = ops.StateConflictError

var ErrStateConflict = ops.ErrStateConflict

type Store struct {
	db                         *sql.DB
	dir                        string
	readOnly                   bool
	previewHost, previewLaunch ops.Lock
	cleanup                    func() error
}

// Open requires an existing private runner state directory. The connection pool
// is deliberately one connection; WAL still permits other runner processes to
// read while a writer commits. Immediate transactions avoid lock-upgrade races.
func Open(stateDir string) (*Store, error) {
	return OpenContext(context.Background(), stateDir)
}

// OpenContext applies the caller deadline to control-store initialization.
func OpenContext(ctx context.Context, stateDir string) (*Store, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	dir, err := filepath.Abs(stateDir)
	if err != nil {
		return nil, err
	}
	if err = secureStateDir(dir); err != nil {
		return nil, err
	}
	path := filepath.Join(dir, "control.db")
	for _, file := range []string{path, path + "-wal", path + "-shm"} {
		f, err := openPrivateFile(file)
		if err != nil {
			return nil, err
		}
		if err = f.Close(); err != nil {
			return nil, err
		}
	}
	u := url.URL{Scheme: "file", Path: path}
	q := u.Query()
	for _, p := range []string{"journal_mode(WAL)", "synchronous(FULL)", "foreign_keys(ON)", "busy_timeout(5000)"} {
		q.Add("_pragma", p)
	}
	q.Set("_txlock", "immediate")
	u.RawQuery = q.Encode()
	db, err := sql.Open("sqlite", u.String())
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	s := &Store{db: db, dir: dir}
	if err = s.migrate(ctx); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}
func (s *Store) Close() error {
	err := s.db.Close()
	if s.cleanup != nil {
		err = errors.Join(err, s.cleanup())
	}
	return err
}

func (s *Store) migrate(ctx context.Context) (err error) {
	// SQLite requires foreign keys off outside the transaction while rebuilding
	// a referenced table. They are restored before Open returns on every path.
	if _, err = s.db.ExecContext(ctx, "PRAGMA foreign_keys=OFF"); err != nil {
		return err
	}
	defer func() {
		_, e := s.db.ExecContext(context.WithoutCancel(ctx), "PRAGMA foreign_keys=ON")
		err = errors.Join(err, e)
	}()
	tx, cancel, err := s.beginWrite(ctx)
	defer cancel()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_version (version INTEGER NOT NULL)`); err != nil {
		return err
	}
	var version int
	err = tx.QueryRowContext(ctx, "SELECT version FROM schema_version").Scan(&version)
	if errors.Is(err, sql.ErrNoRows) {
		if _, err = tx.ExecContext(ctx, "INSERT INTO schema_version VALUES(0)"); err != nil {
			return err
		}
		version = 0
	} else if err != nil {
		return err
	}
	var count int
	if err = tx.QueryRowContext(ctx, "SELECT count(*) FROM schema_version").Scan(&count); err != nil {
		return err
	}
	if count != 1 || version < 0 || version > SchemaVersion {
		return &SchemaError{version}
	}
	if version == 0 {
		if _, err = tx.ExecContext(ctx, schema); err != nil {
			return err
		}
		for from, tos := range ops.Transitions() {
			for _, to := range tos {
				if _, err = tx.ExecContext(ctx, "INSERT INTO transitions VALUES(?,?)", from, to); err != nil {
					return err
				}
			}
		}
		if _, err = tx.ExecContext(ctx, "UPDATE schema_version SET version=1"); err != nil {
			return err
		}
	}
	if version < 2 {
		if err = migrateOperations(ctx, tx); err != nil {
			return err
		}
	}
	if version < 3 {
		if err = migrateResolution(ctx, tx); err != nil {
			return err
		}
	}
	if version < 4 {
		if err = migrateData(ctx, tx); err != nil {
			return err
		}
	}
	if version < 5 {
		if err = migrateDataEvidence(ctx, tx); err != nil {
			return err
		}
	}
	if version < 6 {
		if err = migrateRestorePoints(ctx, tx); err != nil {
			return err
		}
	}
	if version < 7 {
		if err = migrateDataInitialization(ctx, tx); err != nil {
			return err

		}
	}
	// Transitions are the current per-kind journal contract. Add new supported
	// edges transactionally without changing durable operation identities.
	for _, kind := range []ops.Kind{ops.Deploy, ops.SecretSet, ops.Reconcile, ops.Resolve} {
		for from, tos := range ops.TransitionsFor(kind) {
			for _, to := range tos {
				if _, err = tx.ExecContext(ctx, "INSERT INTO transitions VALUES(?,?,?) ON CONFLICT DO NOTHING", kind, from, to); err != nil {
					return err
				}
			}

		}
	}
	rows, e := tx.QueryContext(ctx, "PRAGMA foreign_key_check")
	if e != nil {
		return e
	}
	broken := rows.Next()
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	if broken {
		return &IntegrityError{}
	}
	return tx.Commit()
}

const schema = `
CREATE TABLE plans (id TEXT PRIMARY KEY, canonical BLOB NOT NULL, content_hash TEXT NOT NULL, desired BLOB NOT NULL, desired_hash TEXT NOT NULL);
CREATE TABLE transitions (from_state TEXT NOT NULL, to_state TEXT NOT NULL, PRIMARY KEY(from_state,to_state));
CREATE TABLE operations (id TEXT PRIMARY KEY, plan_id TEXT NOT NULL REFERENCES plans(id), requester TEXT NOT NULL, idempotency_key TEXT NOT NULL, state TEXT NOT NULL CHECK(state IN ('queued','launch_unknown','preflight','preparing','quiescing','starting','checking','committing','rolling_back','succeeded','failed','rolled_back','recovery_required')), created_at TEXT NOT NULL, updated_at TEXT NOT NULL, UNIQUE(requester,idempotency_key));
CREATE TRIGGER legal_transition BEFORE UPDATE OF state ON operations WHEN OLD.state<>NEW.state AND NOT EXISTS (SELECT 1 FROM transitions WHERE from_state=OLD.state AND to_state=NEW.state) BEGIN SELECT RAISE(ABORT,'illegal state transition'); END;
CREATE TABLE events (operation_id TEXT NOT NULL REFERENCES operations(id), seq INTEGER NOT NULL CHECK(seq>0), kind TEXT NOT NULL, state TEXT NOT NULL, payload BLOB NOT NULL CHECK(length(payload)<=4096), created_at TEXT NOT NULL, PRIMARY KEY(operation_id,seq));
CREATE TRIGGER events_no_update BEFORE UPDATE ON events BEGIN SELECT RAISE(ABORT,'append-only events'); END;
CREATE TRIGGER events_no_delete BEFORE DELETE ON events BEGIN SELECT RAISE(ABORT,'append-only events'); END;
CREATE TRIGGER events_order BEFORE INSERT ON events WHEN NEW.seq<>COALESCE((SELECT MAX(seq)+1 FROM events WHERE operation_id=NEW.operation_id),1) BEGIN SELECT RAISE(ABORT,'event sequence'); END;
CREATE TABLE releases (app TEXT NOT NULL, id TEXT NOT NULL, plan_id TEXT NOT NULL REFERENCES plans(id), content BLOB NOT NULL, content_hash TEXT NOT NULL, committed_at TEXT NOT NULL, PRIMARY KEY(app,id));
CREATE TABLE release_heads (app TEXT PRIMARY KEY, current_id TEXT NOT NULL, previous_id TEXT, FOREIGN KEY(app,current_id) REFERENCES releases(app,id), FOREIGN KEY(app,previous_id) REFERENCES releases(app,id));
CREATE TRIGGER releases_no_update BEFORE UPDATE ON releases BEGIN SELECT RAISE(ABORT,'immutable releases'); END;
CREATE TRIGGER releases_no_delete BEFORE DELETE ON releases BEGIN SELECT RAISE(ABORT,'immutable releases'); END;
`

var digestPattern = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)

func digest(b []byte) string { sum := sha256.Sum256(b); return "sha256:" + hex.EncodeToString(sum[:]) }

func (s *Store) SavePlan(ctx context.Context, p plan.Plan, d policy.Desired) (PlanID, error) {
	b, err := p.CanonicalBytes()
	if err != nil {
		return "", ErrInvalid
	}
	desired, err := d.CanonicalBytes()
	if err != nil {
		return "", ErrInvalid
	}
	if len(b) > MaxPlanBytes || len(desired) > MaxPlanBytes || p.SchemaVersion != plan.SchemaVersion || !digestPattern.MatchString(p.Hash) || p.DesiredHash != digest(desired) || p.App != string(d.Name) || p.PolicyHash != d.PolicyHash || p.PolicyVersion != d.PolicyVersion {
		return "", &IntegrityError{}
	}
	// Plan serialization must never accidentally carry literal environment values.
	for _, change := range p.Changes {
		if change.Quadlet != nil && len(change.Quadlet.Desired.Environment) != 0 {
			return "", ErrInvalid
		}
	}
	tx, cancel, err := s.beginWrite(ctx)
	defer cancel()
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	_, err = tx.ExecContext(ctx, "INSERT INTO plans VALUES(?,?,?,?,?) ON CONFLICT(id) DO NOTHING", p.Hash, b, digest(b), desired, p.DesiredHash)
	if err != nil {
		return "", err
	}
	var oldB, oldD []byte
	var oldHash, oldDH string
	if err = tx.QueryRowContext(ctx, "SELECT canonical,content_hash,desired,desired_hash FROM plans WHERE id=?", p.Hash).Scan(&oldB, &oldHash, &oldD, &oldDH); err != nil {
		return "", err
	}
	if !bytes.Equal(b, oldB) || !bytes.Equal(desired, oldD) || oldHash != digest(b) || oldDH != p.DesiredHash {
		return "", ErrConflict
	}
	return p.Hash, tx.Commit()
}
func loadPlan(ctx context.Context, q interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, id PlanID) (plan.Plan, policy.Desired, error) {
	var p plan.Plan
	var d policy.Desired
	var b, raw []byte
	var checksum, dh string
	err := q.QueryRowContext(ctx, "SELECT canonical,content_hash,desired,desired_hash FROM plans WHERE id=?", id).Scan(&b, &checksum, &raw, &dh)
	if errors.Is(err, sql.ErrNoRows) {
		return p, d, ErrNotFound
	}
	if err != nil {
		return p, d, err
	}
	if len(b) > MaxPlanBytes || len(raw) > MaxPlanBytes || digest(b) != checksum || digest(raw) != dh || json.Unmarshal(b, &p) != nil || json.Unmarshal(raw, &d) != nil {
		return plan.Plan{}, policy.Desired{}, &IntegrityError{}
	}
	canonical, _ := p.CanonicalBytes()
	normalized, _ := d.CanonicalBytes()
	if !bytes.Equal(b, canonical) || !bytes.Equal(raw, normalized) || p.Hash != id || p.DesiredHash != dh || p.SchemaVersion != plan.SchemaVersion || p.App != string(d.Name) || p.PolicyHash != d.PolicyHash || p.PolicyVersion != d.PolicyVersion {
		return plan.Plan{}, policy.Desired{}, &IntegrityError{}
	}
	return p, d, nil
}
func (s *Store) LoadPlan(ctx context.Context, id PlanID) (plan.Plan, policy.Desired, error) {
	return loadPlan(ctx, s.db, id)
}
