package data

import (
	"context"
	"database/sql"
	"maps"
	"slices"
	"strings"
)

// SQLite derives unaliased view column names from expression source text.
// Token normalization may change those names even when values are identical.
// Compare with the original DDL and keep its exact text when names differ.
func preserveViewColumns(ctx context.Context, tx *sql.Tx, entries []schemaEntry) (string, bool, error) {
	isView := func(entry schemaEntry) bool {
		return strings.HasPrefix(strings.ToUpper(entry.statement), "CREATE VIEW ")
	}
	if !slices.ContainsFunc(entries, func(entry schemaEntry) bool { return isView(entry) && entry.original != entry.statement }) {
		return "", false, nil
	}
	reference, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		return "", false, err
	}
	defer func() { _ = reference.Close() }()
	reference.SetMaxOpenConns(1)
	originalTx, err := reference.BeginTx(ctx, nil)
	if err != nil {
		return "", false, err
	}
	defer func() { _ = originalTx.Rollback() }()
	for _, entry := range entries {
		statement := entry.statement
		if isView(entry) {
			statement = entry.original
		}
		if _, err := originalTx.ExecContext(ctx, statement); err != nil {
			return "", false, err
		}
	}
	original, err := viewColumnNames(ctx, originalTx)
	if err != nil {
		return "", false, err
	}
	normalized, err := viewColumnNames(ctx, tx)
	if err != nil {
		return "", false, err
	}
	if maps.EqualFunc(original, normalized, func(a, b []string) bool { return slices.Equal(a, b) }) {
		return "", false, nil
	}
	hash, err := initializerCatalog(ctx, originalTx)
	if err != nil {
		return "", false, err
	}
	if err := finishInitializer(ctx, originalTx, "compiler", hash); err != nil {
		return "", false, err
	}
	for i := range entries {
		if isView(entries[i]) {
			entries[i].statement = entries[i].original
		}
	}
	return hash, true, nil
}

type schemaQueryer interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

func viewColumnNames(ctx context.Context, db schemaQueryer) (map[string][]string, error) {
	rows, err := db.QueryContext(ctx, "SELECT name FROM sqlite_schema WHERE type='view' ORDER BY name")
	if err != nil {
		return nil, err
	}
	names := []string{}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			_ = rows.Close()
			return nil, err
		}
		names = append(names, name)
	}
	err = rows.Err()
	_ = rows.Close()
	if err != nil {
		return nil, err
	}
	result := make(map[string][]string, len(names))
	for _, name := range names {
		columns, err := db.QueryContext(ctx, "SELECT name FROM pragma_table_info(?) ORDER BY cid", name)
		if err != nil {
			return nil, err
		}
		result[name] = []string{}
		for columns.Next() {
			var column string
			if err := columns.Scan(&column); err != nil {
				_ = columns.Close()
				return nil, err
			}
			result[name] = append(result[name], column)
		}
		err = columns.Err()
		_ = columns.Close()
		if err != nil {
			return nil, err
		}
	}
	return result, nil
}
