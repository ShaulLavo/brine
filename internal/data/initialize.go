package data

import (
	"context"
	"database/sql"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// SchemaInitializer is an operator-reviewed artifact, not a deploy/request SQL
// payload. Its canonical digest is bound by the initialization plan. Callers
// must authorize that artifact, hold the mutation/fence/lifetime locks, and
// durably record intent and an independently verified empty restore point first.
type SchemaInitializer struct {
	Definition SQLiteSchemaDefinition `json:"definition"`
	Statements []string               `json:"statements"`
}

func (a SchemaInitializer) Validate(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if !ValidMarker(a.Definition.Marker) || a.Definition.Marker == EmptyMarker || !ValidCatalogHash(a.Definition.CatalogSHA256) || !namePattern.MatchString(string(a.Definition.Database)) || len(a.Statements) == 0 || len(a.Statements) > 128 {
		return ErrInvalid
	}
	entries, err := initializerEntries(a.Statements)
	if err != nil {
		return ErrInvalid
	}
	hash, err := compileInitializer(ctx, entries)
	if err != nil || hash != a.Definition.CatalogSHA256 {
		return ErrInvalid
	}

	return nil
}

// InitializeSchema never retries or repairs a database. A failed create can
// leave a zero-byte file; a failed/unknown commit must be inspected by the
// journal owner, never replayed. App schema and marker commit atomically.
func InitializeSchema(ctx context.Context, b SQLiteDatabaseBinding, a SchemaInitializer, allocation *AllocationReceipt) error {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if a.Definition.Database != b.Name {
		return ErrInvalid
	}
	if err := a.Validate(ctx); err != nil {
		return err
	}
	o := ObserveSQLiteSchemaWithAllocation(ctx, b, nil, allocation)
	if o.State != AllocatedEmpty && o.State != VerifiedEmpty {
		return ErrInvalid
	}
	source := filepath.Join(string(b.Root), b.RelativeDirectory)
	p := filepath.Join(source, string(b.Filename))
	if o.State == AllocatedEmpty {
		f, err := os.OpenFile(p, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600) // #nosec G304 -- ObserveSchemaWithAllocation validates the bound private tree and exact database filename.
		if err != nil {
			return err
		}
		err = errors.Join(f.Sync(), f.Close())
		if err != nil {
			return err
		}
		dir, err := os.Open(source) // #nosec G304 -- The allocation receipt verifies this exact private directory.
		if err != nil {
			return err
		}
		err = errors.Join(dir.Sync(), dir.Close())
		if err != nil {
			return err
		}
	}
	before, err := verifyPrivateFile(p)
	if err != nil {
		return err
	}
	u := url.URL{Scheme: "file", Path: p}
	q := u.Query()
	q.Set("mode", "rw")
	q.Set("_txlock", "immediate")
	for _, pragma := range []string{"busy_timeout(5000)", "synchronous(FULL)", "foreign_keys(ON)"} {
		q.Add("_pragma", pragma)
	}
	u.RawQuery = q.Encode()
	db, err := sql.Open("sqlite", u.String())
	if err != nil {
		return err
	}
	defer func() { _ = db.Close() }()
	db.SetMaxOpenConns(1)
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	hash, err := initializerCatalog(ctx, tx)
	if err != nil || hash != EmptyCatalogSHA256 {
		return ErrInvalid
	}
	if err = applyInitializer(ctx, tx, a); err != nil {
		return err
	}
	after, err := verifyPrivateFile(p)
	if err != nil || !os.SameFile(before, after) {
		return ErrInvalid
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	after, err = verifyPrivateFile(p)
	if err != nil || !os.SameFile(before, after) {
		return ErrInvalid
	}
	return nil
}

func applyInitializer(ctx context.Context, tx *sql.Tx, a SchemaInitializer) error {
	entries, err := initializerEntries(a.Statements)
	if err != nil {
		return err
	}
	if _, err := compileInitializer(ctx, entries); err != nil {
		return err
	}
	for _, entry := range entries {
		if _, err := tx.ExecContext(ctx, entry.statement); err != nil {
			return err
		}
	}
	hash, err := initializerCatalog(ctx, tx)
	if err != nil || hash != a.Definition.CatalogSHA256 {
		return ErrInvalid
	}
	return finishInitializer(ctx, tx, a.Definition.Marker, hash)
}

func finishInitializer(ctx context.Context, tx *sql.Tx, marker, hash string) error {
	if _, err := tx.ExecContext(ctx, MarkerTableSQL); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, "INSERT INTO brine_schema_marker VALUES(1,?,?)", marker, hash); err != nil {
		return err
	}
	var integrity string
	if err := tx.QueryRowContext(ctx, "PRAGMA integrity_check(1)").Scan(&integrity); err != nil || integrity != "ok" {
		return ErrInvalid
	}
	rows, err := tx.QueryContext(ctx, "PRAGMA foreign_key_check")
	if err != nil {
		return err
	}
	defer func() { _ = rows.Close() }()
	if rows.Next() || rows.Err() != nil {
		return ErrInvalid
	}
	return nil
}

func initializerCatalog(ctx context.Context, tx *sql.Tx) (string, error) {
	catalog, err := initializerCatalogRows(ctx, tx)
	if err != nil {
		return "", err
	}
	_, hash, err := CatalogFingerprint(catalog)
	return hash, err
}

// InitializerCatalog returns the actual application catalog for plan review.
func InitializerCatalog(ctx context.Context, a SchemaInitializer) ([]CatalogRow, error) {
	if err := a.Validate(ctx); err != nil {
		return nil, err
	}
	entries, err := initializerEntries(a.Statements)
	if err != nil {
		return nil, err
	}
	if _, err := compileInitializer(ctx, entries); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		return nil, err
	}
	defer func() { _ = db.Close() }()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	for _, entry := range entries {
		if _, err := tx.ExecContext(ctx, entry.statement); err != nil {
			return nil, err
		}
	}
	return initializerCatalogRows(ctx, tx)
}

func initializerCatalogRows(ctx context.Context, tx *sql.Tx) ([]CatalogRow, error) {
	rows, err := tx.QueryContext(ctx, "SELECT type,name,tbl_name,sql FROM sqlite_schema ORDER BY type COLLATE BINARY,name COLLATE BINARY")
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	catalog := []CatalogRow{}
	for rows.Next() {
		var row CatalogRow
		var statement sql.NullString
		if err = rows.Scan(&row.Type, &row.Name, &row.TableName, &statement); err != nil {
			return nil, err
		}
		if statement.Valid {
			row.SQL = &statement.String
		}
		if strings.HasPrefix(row.Name, "sqlite_") {
			continue
		}
		if row.Name == "_litestream_seq" || row.Name == "_litestream_lock" {
			expected := "CREATE TABLE _litestream_seq (id INTEGER PRIMARY KEY, seq INTEGER)"
			if row.Name == "_litestream_lock" {
				expected = "CREATE TABLE _litestream_lock (id INTEGER)"
			}
			if row.Type != "table" || row.Name != row.TableName || row.SQL == nil || *row.SQL != expected {
				return nil, ErrInvalid
			}
			continue
		}
		if row.Name == "brine_schema_marker" || row.TableName == "brine_schema_marker" || strings.HasPrefix(row.TableName, "_litestream_") {
			return nil, ErrInvalid
		}
		catalog = append(catalog, row)
		if len(catalog) > maxCatalogRows {
			return nil, ErrInvalid
		}
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	return catalog, nil
}
