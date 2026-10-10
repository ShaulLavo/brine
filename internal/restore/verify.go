package restore

import (
	"context"
	"database/sql"
	"net/url"
	"slices"
	"strconv"
	"time"

	_ "modernc.org/sqlite"
)

func verify(ctx context.Context, path string, observer SchemaObserver, r Request) (observation SchemaObservation, sentinel *Sentinel, resultErr error) {
	u := url.URL{Scheme: "file", Path: path}
	query := u.Query()
	query.Set("mode", "ro")
	u.RawQuery = query.Encode()
	db, err := sql.Open("sqlite", u.String())
	if err != nil {
		return SchemaObservation{}, nil, refuse("sqlite_open")
	}
	defer func() {
		if err := db.Close(); err != nil && resultErr == nil {
			observation, sentinel, resultErr = SchemaObservation{}, nil, refuse("sqlite_close")
		}
	}()
	db.SetMaxOpenConns(1)
	// query_only also makes the injected schema observer unable to mutate data.
	if _, err := db.ExecContext(ctx, "PRAGMA query_only=ON"); err != nil {
		return SchemaObservation{}, nil, refuse("sqlite_read_only")
	}
	tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return SchemaObservation{}, nil, refuse("sqlite_transaction")
	}
	defer func() { _ = tx.Rollback() }() // Failure cleanup for a read-only transaction; committed transactions return ErrTxDone.
	integrity, err := tx.QueryContext(ctx, "PRAGMA integrity_check")
	if err != nil {
		return SchemaObservation{}, nil, refuse("integrity_check")
	}
	count := 0
	for integrity.Next() {
		var result string
		if err := integrity.Scan(&result); err != nil || result != "ok" {
			_ = integrity.Close() // Preserve the already observed integrity failure.
			return SchemaObservation{}, nil, refuse("integrity_check")
		}
		count++
	}
	err = integrity.Err()
	closeErr := integrity.Close()
	if err != nil || closeErr != nil || count != 1 {
		return SchemaObservation{}, nil, refuse("integrity_check")
	}
	foreign, err := tx.QueryContext(ctx, "PRAGMA foreign_key_check")
	if err != nil {
		return SchemaObservation{}, nil, refuse("foreign_key_check")
	}
	invalid := foreign.Next()
	err = foreign.Err()
	closeErr = foreign.Close()
	if invalid || err != nil || closeErr != nil {
		return SchemaObservation{}, nil, refuse("foreign_key_check")
	}
	schema, err := observer.Observe(ctx, tx)
	if err != nil || !validSchema(schema) || !(schema == r.ExpectedSchema || slices.Contains(r.AcceptedSchemas, schema)) {
		return SchemaObservation{}, nil, refuse("schema_state_unknown_or_mismatch")
	}
	for _, check := range r.Invariants {
		if err := checkInvariant(ctx, tx, check); err != nil {
			return SchemaObservation{}, nil, err
		}
	}
	if expected := r.Sentinel; expected != nil {
		if err := requireColumns(ctx, tx, expected.Table, []string{"sequence", "marker", "committed_at"}); err != nil {
			return SchemaObservation{}, nil, err
		}
		var marker, committed string
		// #nosec G202 -- validateChecks restricts the table to an identifier; requireColumns verifies a real table. Values remain bound parameters.
		rows, err := tx.QueryContext(ctx, `SELECT marker, committed_at FROM `+quoted(expected.Table)+` WHERE sequence = ?`, expected.Sequence)
		if err != nil {
			return SchemaObservation{}, nil, refuse("sentinel_check")
		}
		if !rows.Next() {
			_ = rows.Close() // Preserve the missing-sentinel failure.
			return SchemaObservation{}, nil, refuse("sentinel_missing")
		}
		err = rows.Scan(&marker, &committed)
		duplicate := rows.Next()
		rowsErr := rows.Err()
		closeErr = rows.Close()
		instant, parseErr := time.Parse(time.RFC3339Nano, committed)
		if err != nil || rowsErr != nil || closeErr != nil || duplicate || parseErr != nil || marker != expected.Marker || !instant.Equal(expected.CommittedAt) || instant.After(time.Now()) {
			return SchemaObservation{}, nil, refuse("sentinel_mismatch")
		}
		sentinel = &Sentinel{Table: expected.Table, Sequence: expected.Sequence, Marker: marker, CommittedAt: instant.UTC()}
	}
	if err := tx.Commit(); err != nil {
		return SchemaObservation{}, nil, refuse("sqlite_transaction")
	}
	return schema, sentinel, nil
}
func quoted(identifier string) string { return `"` + identifier + `"` }
func checkInvariant(ctx context.Context, tx *sql.Tx, c Invariant) error {
	columns := []string{}
	if c.Column != "" {
		columns = append(columns, c.Column)
	}
	if err := requireColumns(ctx, tx, c.Table, columns); err != nil {
		return err
	}
	table := quoted(c.Table)
	query := `SELECT count(*) FROM ` + table
	expected := int64(0)
	switch c.Kind {
	case RowCount:
		expected = c.Count
	case NonNull:
		query += ` WHERE ` + quoted(c.Column) + ` IS NULL`
	case IntegerRange:
		column := quoted(c.Column)
		query += ` WHERE typeof(` + column + `) != 'integer' OR ` + column + ` < ` + strconv.FormatInt(c.Minimum, 10) + ` OR ` + column + ` > ` + strconv.FormatInt(c.Maximum, 10)
	default:
		return refuse("invalid_invariant")
	}
	var result int64
	if err := tx.QueryRowContext(ctx, query).Scan(&result); err != nil || result != expected {
		return refuse("invariant_failed")
	}
	return nil
}

func requireColumns(ctx context.Context, tx *sql.Tx, table string, columns []string) (resultErr error) {
	var kind string
	if err := tx.QueryRowContext(ctx, `SELECT type FROM sqlite_schema WHERE name = ?`, table).Scan(&kind); err != nil || kind != "table" {
		return refuse("invariant_table_missing")
	}
	rows, err := tx.QueryContext(ctx, `SELECT name FROM pragma_table_xinfo(?)`, table)
	if err != nil {
		return refuse("invariant_column_missing")
	}
	defer func() {
		if err := rows.Close(); err != nil && resultErr == nil {
			resultErr = refuse("invariant_column_missing")
		}
	}()
	found := make(map[string]bool)
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return refuse("invariant_column_missing")
		}
		found[name] = true
	}
	if rows.Err() != nil {
		return refuse("invariant_column_missing")
	}
	for _, column := range columns {
		if !found[column] {
			return refuse("invariant_column_missing")
		}
	}
	return nil
}
