package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/url"
	"os"
	"path/filepath"
	"time"

	"github.com/ShaulLavo/brine/internal/ops"
)

// OpenReadOnly neither creates state files nor changes permissions or schema.
// SQLite may use existing WAL shared memory, but SQL writes are refused.
func OpenReadOnly(ctx context.Context, stateDir string) (*Store, error) {
	dir, err := filepath.Abs(stateDir)
	if err != nil {
		return nil, err
	}
	for _, path := range []string{dir, filepath.Join(dir, "control.db")} {
		if e := readOnlyOwner(path); e != nil {
			return nil, e
		}
		info, e := os.Lstat(path)
		if e != nil {
			return nil, e
		}
		if info.Mode()&os.ModeSymlink != 0 || (path == dir && !info.IsDir()) || (path != dir && !info.Mode().IsRegular()) || info.Mode().Perm()&0077 != 0 {
			return nil, ErrInvalid
		}
	}
	u := url.URL{Scheme: "file", Path: filepath.Join(dir, "control.db")}
	q := u.Query()
	q.Set("mode", "ro")
	q.Add("_pragma", "query_only(ON)")
	q.Add("_pragma", "busy_timeout(100)")
	u.RawQuery = q.Encode()
	db, err := sql.Open("sqlite", u.String())
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	var count, version int
	err = db.QueryRowContext(ctx, "SELECT count(*), COALESCE(max(version),-1) FROM schema_version").Scan(&count, &version)
	if err != nil || count != 1 || version != SchemaVersion {
		db.Close()
		if err != nil {
			return nil, err
		}
		return nil, &SchemaError{version}
	}
	return &Store{db: db, dir: dir, readOnly: true}, nil
}

// AppNames includes committed apps and apps with pending or failed operations.
// Fetching one extra name lets diagnose report truncation without an unbounded inventory.
func (s *Store) AppNames(ctx context.Context) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT app FROM release_heads UNION SELECT json_extract(canonical,'$.app') FROM plans ORDER BY 1 LIMIT 17`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	names := []string{}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		names = append(names, name)
	}
	return names, rows.Err()
}
func (s *Store) RecentOperations(ctx context.Context, app string, limit int) ([]ops.OperationRecord, error) {
	if limit < 1 || limit > 10 {
		return nil, ErrInvalid
	}
	rows, err := s.db.QueryContext(ctx, `SELECT o.id,o.plan_id,o.requester,o.idempotency_key,o.state,o.created_at,o.updated_at,
 COALESCE((SELECT json_object('kind',kind,'payload',json(payload)) FROM events e WHERE e.operation_id=o.id AND (e.kind='failure' OR (e.kind='step' AND json_extract(e.payload,'$.outcome') IN ('failed','unknown') AND json_extract(e.payload,'$.code') IS NOT NULL)) ORDER BY seq DESC LIMIT 1),'')
 FROM operations o JOIN plans p ON p.id=o.plan_id WHERE json_extract(p.canonical,'$.app')=? ORDER BY julianday(o.updated_at) DESC,o.updated_at DESC,o.id DESC LIMIT ?`, app, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	records := []ops.OperationRecord{}
	for rows.Next() {
		var record ops.OperationRecord
		var created, updated string
		var payload []byte
		o := &record.Operation
		if err = rows.Scan(&o.ID, &o.PlanID, &o.Requester, &o.IdempotencyKey, &o.State, &created, &updated, &payload); err != nil {
			return nil, err
		}
		// Match the journal reader's timestamp and state validation without exposing payload text.
		o.CreatedAt, err = time.Parse(time.RFC3339Nano, created)
		if err != nil {
			return nil, &IntegrityError{}
		}
		o.UpdatedAt, err = time.Parse(time.RFC3339Nano, updated)
		if err != nil || !ops.ValidState(o.State) {
			return nil, &IntegrityError{}
		}
		if len(payload) > 0 {
			var event ops.Event
			if json.Unmarshal(payload, &event) != nil || ops.ValidateEvent(event) != nil {
				return nil, &IntegrityError{}
			}
			var p ops.FailurePayload
			if json.Unmarshal(event.Payload, &p) != nil {
				return nil, &IntegrityError{}
			}
			record.FailureCode = p.Code
		}
		records = append(records, record)
	}
	return records, rows.Err()
}

// ReadGeneration leaves an affirmatively empty enrolled state directory empty.
// Unknown contents and an existing damaged database never become generation zero.
func ReadGeneration(ctx context.Context, stateDir string) (uint64, error) {
	dir, err := filepath.Abs(stateDir)
	if err != nil {
		return 0, err
	}
	if err = secureStateDir(dir); err != nil {
		return 0, err
	}
	if _, err = os.Lstat(filepath.Join(dir, "control.db")); os.IsNotExist(err) {
		entries, e := os.ReadDir(dir)
		if e != nil {
			return 0, e
		}
		if len(entries) != 0 {
			return 0, ErrInvalid
		}
		return 0, nil
	} else if err != nil {
		return 0, err
	}
	s, err := OpenReadOnly(ctx, dir)
	if err != nil {
		return 0, err
	}
	defer s.Close()
	return s.Generation(ctx)
}
