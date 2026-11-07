package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
)

// FileID describes captured bytes, not permission to replace a host file.
type FileID struct {
	Dev, Ino uint64
	Size     int64
	SHA256   string
}

// Capture preserves DB and WAL presence. A nil WAL is absent; a non-nil empty
// WAL is present. SHM is disposable and is never recovery data.
type Capture struct {
	Absent      bool
	DB, WAL     []byte
	DBID, WALID FileID
}

// SchemaFacts are observations of an isolated copy, not host authority.
type SchemaFacts struct {
	Absent                 bool
	Version                int
	Integrity, ForeignKeys bool
}

// InspectCapture accepts older and newer schema versions without migrating.
// scratch is a caller-selected workspace; each call owns a private child only.
func InspectCapture(ctx context.Context, c Capture, scratch string) (f SchemaFacts, err error) {
	if err = validateControlCapture(ctx, c); err != nil {
		return f, err
	}
	if c.Absent {
		return SchemaFacts{Absent: true}, nil
	}
	dir, err := stageControlCapture(ctx, c, scratch)
	if err != nil {
		return f, err
	}
	defer func() { err = errors.Join(err, os.RemoveAll(dir)) }()
	db, err := openControlCapture(ctx, filepath.Join(dir, "control.db"), true)
	if err != nil {
		return f, err
	}
	defer func() { err = errors.Join(err, db.Close()) }()
	return inspectControlSchema(ctx, db)
}

// RehearseMigration runs the actual Store migration on a private copy. Its
// normalized output is not a verified restore candidate; A3 owns row validation.
func RehearseMigration(ctx context.Context, c Capture, scratch string) (migrated []byte, f SchemaFacts, err error) {
	if err = validateControlCapture(ctx, c); err != nil {
		return nil, f, err
	}
	if c.Absent {
		return nil, SchemaFacts{Absent: true}, nil
	}
	dir, err := stageControlCapture(ctx, c, scratch)
	if err != nil {
		return nil, f, err
	}
	defer func() {
		err = errors.Join(err, os.RemoveAll(dir))
		if err != nil {
			migrated = nil
		}
	}()
	db, err := openControlCapture(ctx, filepath.Join(dir, "control.db"), false)
	if err != nil {
		return nil, f, err
	}
	defer func() { err = errors.Join(err, db.Close()) }()
	f, err = inspectControlSchema(ctx, db)
	if err != nil {
		return nil, f, err
	}
	if !f.Integrity || !f.ForeignKeys {
		return nil, f, &IntegrityError{}
	}
	if f.Version > SchemaVersion {
		return nil, f, &SchemaError{Version: f.Version}
	}
	s := &Store{db: db, dir: dir}
	if err = s.migrate(ctx); err != nil {
		return nil, f, err
	}
	f, err = inspectControlSchema(ctx, db)
	if err != nil {
		return nil, f, err
	}
	if f.Version != SchemaVersion || !f.Integrity || !f.ForeignKeys {
		return nil, f, &IntegrityError{}
	}
	return normalizeControlCapture(ctx, db, dir)
}

func controlDigest(data []byte) string {
	hash := sha256.Sum256(data)
	return hex.EncodeToString(hash[:])
}

func validateControlCapture(ctx context.Context, c Capture) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if len(c.DB) > maxPreviewBytes || len(c.WAL) > maxPreviewBytes-len(c.DB) {
		return ErrPreviewUnavailable
	}
	if c.Absent {
		if c.DB != nil || c.WAL != nil || c.DBID != (FileID{}) || c.WALID != (FileID{}) {
			return ErrInvalid
		}
		return nil
	}
	if c.DB == nil || !controlBytesMatch(c.DB, c.DBID) {
		return ErrInvalid
	}
	if c.WAL == nil {
		if c.WALID != (FileID{}) {
			return ErrInvalid
		}
		return nil
	}
	if !controlBytesMatch(c.WAL, c.WALID) {
		return ErrInvalid
	}
	return ctx.Err()
}

func controlBytesMatch(data []byte, id FileID) bool {
	return id.Size == int64(len(data)) && id.SHA256 == controlDigest(data)
}

func stageControlCapture(ctx context.Context, c Capture, scratch string) (_ string, err error) {
	dir, err := os.MkdirTemp(scratch, "brine-control-")
	if err != nil {
		return "", err
	}
	defer func() {
		if err != nil {
			err = errors.Join(err, os.RemoveAll(dir))
		}
	}()
	if err = writeControlCopy(ctx, filepath.Join(dir, "control.db"), c.DB); err != nil {
		return "", err
	}
	if c.WAL != nil {
		if err = writeControlCopy(ctx, filepath.Join(dir, "control.db-wal"), c.WAL); err != nil {
			return "", err
		}
	}
	return dir, nil
}

func writeControlCopy(ctx context.Context, path string, data []byte) (err error) {
	if err = ctx.Err(); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600) // #nosec G304 -- Fixed name inside this call's fresh private scratch directory.
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, f.Close()) }()
	for len(data) > 0 {
		if err = ctx.Err(); err != nil {
			return err
		}
		n := min(len(data), 64<<10)
		written, e := f.Write(data[:n])
		if e != nil {
			return e
		}
		if written != n {
			return io.ErrShortWrite
		}
		data = data[n:]
	}
	return ctx.Err()
}

func openControlCapture(ctx context.Context, path string, readOnly bool) (_ *sql.DB, err error) {
	u := url.URL{Scheme: "file", Path: path}
	q := u.Query()
	q.Set("mode", "rw")
	q.Add("_pragma", "busy_timeout(100)")
	q.Add("_pragma", "temp_store(MEMORY)")
	if readOnly {
		q.Set("mode", "ro")
		q.Add("_pragma", "query_only(ON)")
	}
	u.RawQuery = q.Encode()
	db, err := sql.Open("sqlite", u.String())
	if err != nil {
		return nil, err
	}
	defer func() {
		if err != nil {
			err = errors.Join(err, db.Close())
		}
	}()
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	if readOnly {
		return db, nil
	}
	var pageSize int
	if err = db.QueryRowContext(ctx, "PRAGMA page_size").Scan(&pageSize); err != nil {
		return nil, err
	}
	if pageSize < 512 || pageSize > 65536 {
		return nil, ErrInvalid
	}
	_, err = db.ExecContext(ctx, fmt.Sprintf("PRAGMA max_page_count=%d", maxPreviewBytes/pageSize))
	if err != nil {
		return nil, err
	}
	return db, nil
}

func inspectControlSchema(ctx context.Context, db *sql.DB) (f SchemaFacts, err error) {
	var count int
	if err = db.QueryRowContext(ctx, "SELECT count(*), COALESCE(max(version),-1) FROM schema_version").Scan(&count, &f.Version); err != nil {
		return f, err
	}
	if count != 1 || f.Version < 0 {
		return f, &SchemaError{Version: f.Version}
	}
	var integrity string
	if err = db.QueryRowContext(ctx, "PRAGMA integrity_check(1)").Scan(&integrity); err != nil {
		return f, err
	}
	f.Integrity = integrity == "ok"
	rows, err := db.QueryContext(ctx, "PRAGMA foreign_key_check")
	if err != nil {
		return f, err
	}
	defer func() { err = errors.Join(err, rows.Close()) }()
	f.ForeignKeys = !rows.Next()
	return f, rows.Err()
}

func normalizeControlCapture(ctx context.Context, db *sql.DB, dir string) (data []byte, f SchemaFacts, err error) {
	output := filepath.Join(dir, "normalized.db")
	if _, err = db.ExecContext(ctx, "VACUUM INTO ?", output); err != nil {
		return nil, f, err
	}
	read, err := openControlCapture(ctx, output, true)
	if err != nil {
		return nil, f, err
	}
	defer func() { err = errors.Join(err, read.Close()) }()
	f, err = inspectControlSchema(ctx, read)
	if err != nil {
		return nil, f, err
	}
	if f.Version != SchemaVersion || !f.Integrity || !f.ForeignKeys {
		return nil, f, &IntegrityError{}
	}
	data, err = readControlOutput(ctx, output)
	return data, f, err
}

func readControlOutput(ctx context.Context, path string) (_ []byte, err error) {
	f, err := os.Open(path) // #nosec G304 -- Fixed VACUUM output inside this call's fresh private scratch directory.
	if err != nil {
		return nil, err
	}
	defer func() { err = errors.Join(err, f.Close()) }()
	return readControlBytes(ctx, f, maxPreviewBytes)
}

func readControlBytes(ctx context.Context, f *os.File, limit int64) ([]byte, error) {
	data := make([]byte, 0)
	buffer := make([]byte, 64<<10)
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		n, err := f.Read(buffer[:min(int64(len(buffer)), limit-int64(len(data))+1)])
		data = append(data, buffer[:n]...)
		if int64(len(data)) > limit {
			return nil, ErrPreviewUnavailable
		}
		if errors.Is(err, io.EOF) {
			return data, ctx.Err()
		}
		if err != nil {
			return nil, err
		}
	}
}
