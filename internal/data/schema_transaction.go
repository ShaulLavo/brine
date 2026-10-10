package data

import (
	"context"
	"database/sql"
	"strings"
	"time"
)

// ObserveSchemaTransaction uses the caller's read-only transaction and never
// opens a live path. The caller owns integrity, source containment and lifetime.
func ObserveSchemaTransaction(ctx context.Context, tx *sql.Tx, database DatabaseName, definitions []SchemaDefinition) SchemaObservation {
	o := SchemaObservation{State: Unknown, ObservedAt: time.Now().UTC(), UnknownReason: "unreadable"}
	if tx == nil {
		return o
	}
	var err error
	o.UnknownReason = "integrity"
	var integrity string
	if err = tx.QueryRowContext(ctx, "PRAGMA integrity_check(1)").Scan(&integrity); err != nil || integrity != "ok" {
		return o
	}
	o.UnknownReason = "catalog"
	var count, total int64
	if err = tx.QueryRowContext(ctx, "SELECT count(*),COALESCE(sum(length(type)+length(name)+length(tbl_name)+COALESCE(length(sql),0)),0) FROM sqlite_schema").Scan(&count, &total); err != nil || count > maxCatalogRows || total > maxCatalogBytes {
		return o
	}
	rows, err := tx.QueryContext(ctx, "SELECT type,name,tbl_name,sql FROM sqlite_schema ORDER BY type COLLATE BINARY,name COLLATE BINARY")
	if err != nil {
		return o
	}
	catalog := []CatalogRow{}
	markerFound := false
	internals := map[string]bool{}
	for rows.Next() {
		var r CatalogRow
		var sqlText sql.NullString
		if err = rows.Scan(&r.Type, &r.Name, &r.TableName, &sqlText); err != nil {
			_ = rows.Close()
			return o
		}
		if sqlText.Valid {
			r.SQL = &sqlText.String
		}
		if strings.HasPrefix(r.Name, "sqlite_") {
			continue
		}
		if r.Name == "brine_schema_marker" {
			if r.Type != "table" || r.TableName != r.Name || r.SQL == nil || *r.SQL != MarkerTableSQL {
				_ = rows.Close()
				o.UnknownReason = "marker_structure"
				return o
			}
			markerFound = true
			continue
		}
		if r.Name == "_litestream_seq" || r.Name == "_litestream_lock" {
			expected := "CREATE TABLE _litestream_seq (id INTEGER PRIMARY KEY, seq INTEGER)"
			if r.Name == "_litestream_lock" {
				expected = "CREATE TABLE _litestream_lock (id INTEGER)"
			}
			if r.Type != "table" || r.TableName != r.Name || r.SQL == nil || *r.SQL != expected {
				_ = rows.Close()
				o.UnknownReason = "internal_structure"
				return o
			}
			internals[r.Name] = true
			continue
		}
		catalog = append(catalog, r)
	}
	err = rows.Err()
	_ = rows.Close()
	if err != nil {
		return o
	}
	// Triggers/indexes on internal tables cannot hide behind their table exclusion.
	for _, r := range catalog {
		if r.TableName == "brine_schema_marker" || internals[r.TableName] {
			o.UnknownReason = "internal_structure"
			return o
		}
	}
	_, fingerprint, err := CatalogFingerprint(catalog)
	if err != nil {
		return o
	}
	if len(catalog) == 0 && !markerFound {
		o.State = VerifiedEmpty
		o.Marker = EmptyMarker
		o.CatalogSHA256 = EmptyCatalogSHA256
	} else {
		if !markerFound {
			o.UnknownReason = "marker_missing"
			return o
		}
		var marker, declaredHash string
		var id int64
		rows, err = tx.QueryContext(ctx, "SELECT id,marker,catalog_sha256 FROM brine_schema_marker LIMIT 2")
		if err != nil {
			o.UnknownReason = "marker_structure"
			return o
		}
		valid := rows.Next() && rows.Scan(&id, &marker, &declaredHash) == nil && !rows.Next() && rows.Err() == nil
		_ = rows.Close()
		if !valid || id != 1 || !markerPattern.MatchString(marker) || !hashPattern.MatchString(declaredHash) || marker == EmptyMarker || declaredHash != fingerprint {
			o.UnknownReason = "marker_mismatch"
			return o
		}
		found := false
		for _, d := range definitions {
			if d.Database == database && d.Marker == marker {
				if found || d.CatalogSHA256 != fingerprint {
					o.UnknownReason = "definition_mismatch"
					return o
				}
				found = true
			}
		}
		if !found {
			o.UnknownReason = "definition_missing"
			return o
		}
		o.State = VerifiedSchema
		o.Marker = marker
		o.CatalogSHA256 = fingerprint
	}
	o.UnknownReason = ""
	return o
}
