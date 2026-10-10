package data

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

const MarkerTableSQL = "CREATE TABLE brine_schema_marker (id INTEGER PRIMARY KEY CHECK(id=1), marker TEXT NOT NULL, catalog_sha256 TEXT NOT NULL)"
const maxCatalogRows = 16384
const maxCatalogBytes = 1 << 20

type CatalogRow struct {
	Type      string
	Name      string
	TableName string
	SQL       *string
}

// CatalogFingerprint implements the normative Go JSON serialization. Borrowed
// rows are copied for sorting; SQL NULL stays null and [] is never encoded null.
func CatalogFingerprint(rows []CatalogRow) ([]byte, string, error) {
	if len(rows) > maxCatalogRows {
		return nil, "", ErrInvalid
	}
	sorted := slices.Clone(rows)
	slices.SortFunc(sorted, func(a, b CatalogRow) int {
		if c := strings.Compare(a.Type, b.Type); c != 0 {
			return c
		}
		return strings.Compare(a.Name, b.Name)
	})
	tuples := make([][4]any, 0, len(sorted))
	for _, r := range sorted {
		var value any
		if r.SQL != nil {
			value = *r.SQL
		}
		tuples = append(tuples, [4]any{r.Type, r.Name, r.TableName, value})
	}
	raw, err := json.Marshal(tuples)
	if err != nil {
		return nil, "", err
	}
	if len(raw) > maxCatalogBytes {
		return nil, "", ErrInvalid
	}
	sum := sha256.Sum256(raw)
	return raw, hex.EncodeToString(sum[:]), nil
}

// ObserveSchema never allocates, initializes or repairs a database. An absent
// file cannot be called empty here; allocated_empty needs a store-owned receipt.
func ObserveSchema(ctx context.Context, b DatabaseBinding, definitions []SchemaDefinition) SchemaObservation {
	o := SchemaObservation{State: Unknown, DatabaseID: b.DatabaseID, ObservedAt: time.Now().UTC(), UnknownReason: "unverified_path"}
	relative, err := RelativeDirectory(b.IncarnationID, b.DatabaseID)
	if err != nil || relative != b.RelativeDirectory || !ValidRoot(string(b.Root)) {
		return o
	}
	declaration := Database{Name: b.Name, PersistentRoot: b.Root, MountPath: b.MountPath, Filename: b.Filename, BackupDestination: "observer"}
	if declaration.Validate() != nil {
		return o
	}
	source := filepath.Join(string(b.Root), relative)
	p := filepath.Join(source, string(b.Filename))
	if err = verifyPrivateTree(string(b.Root), source); err != nil {
		return o
	}
	before, err := verifyPrivateFile(p)
	if err != nil {
		if os.IsNotExist(err) {
			o.UnknownReason = "missing"
		} else {
			o.UnknownReason = "unreadable"
		}
		return o
	}
	if before.Size() == 0 {
		o.UnknownReason = "zero_byte"
		return o
	}
	siblings := make(map[string]os.FileInfo)
	for _, suffix := range []string{"-wal", "-shm", "-journal"} {
		info, err := verifyPrivateFile(p + suffix)
		if err == nil {
			siblings[suffix] = info
		} else if !os.IsNotExist(err) {
			o.UnknownReason = "unverified_sibling"
			return o
		}
	}
	u := url.URL{Scheme: "file", Path: p}
	q := u.Query()
	q.Set("mode", "ro")
	q.Add("_pragma", "query_only(ON)")
	q.Add("_pragma", "busy_timeout(5000)")
	u.RawQuery = q.Encode()
	db, err := sql.Open("sqlite", u.String())
	if err != nil {
		o.UnknownReason = "unreadable"
		return o
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		o.UnknownReason = "unreadable"
		return o
	}
	defer tx.Rollback()
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
			rows.Close()
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
				rows.Close()
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
				rows.Close()
				o.UnknownReason = "internal_structure"
				return o
			}
			internals[r.Name] = true
			continue
		}
		catalog = append(catalog, r)
	}
	err = rows.Err()
	rows.Close()
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
		rows.Close()
		if !valid || id != 1 || !markerPattern.MatchString(marker) || !hashPattern.MatchString(declaredHash) || marker == EmptyMarker || declaredHash != fingerprint {
			o.UnknownReason = "marker_mismatch"
			return o
		}
		found := false
		for _, d := range definitions {
			if d.Database == b.Name && d.Marker == marker {
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
	if err = tx.Commit(); err != nil {
		o.State = Unknown
		o.Marker = ""
		o.CatalogSHA256 = ""
		o.UnknownReason = "unreadable"
		return o
	}
	after, err := verifyPrivateFile(p)
	if err != nil || !os.SameFile(before, after) {
		o.State = Unknown
		o.Marker = ""
		o.CatalogSHA256 = ""
		o.UnknownReason = "file_changed"
		return o
	}
	for suffix, info := range siblings {
		after, err = verifyPrivateFile(p + suffix)
		if err != nil || !os.SameFile(info, after) {
			o.State = Unknown
			o.Marker = ""
			o.CatalogSHA256 = ""
			o.UnknownReason = "sibling_changed"
			return o
		}
	}
	o.UnknownReason = ""
	return o
}

// CompatibleObservation checks exact markers only, without inferred ordering.
func CompatibleObservation(database DatabaseName, o SchemaObservation, c SchemaCompatibility) bool {
	if c.Database != database || c.Startup != "preserve" || len(c.Accepts) == 0 || (o.State != VerifiedEmpty && o.State != VerifiedSchema && o.State != AllocatedEmpty) || o.Marker == "" || !hashPattern.MatchString(o.CatalogSHA256) {
		return false
	}
	if (o.State == VerifiedEmpty || o.State == AllocatedEmpty) && (o.Marker != EmptyMarker || o.CatalogSHA256 != EmptyCatalogSHA256) {
		return false
	}
	return slices.Contains(c.Accepts, o.Marker)
}

// WriterCompatible observes each database afresh. The caller resolves bindings
// and declarations from the currently committed release, not a client assertion.
func WriterCompatible(ctx context.Context, bindings []DatabaseBinding, compatibility []SchemaCompatibility, definitions []SchemaDefinition) bool {
	if len(bindings) == 0 {
		return false
	}
	for _, b := range bindings {
		found := false
		for _, c := range compatibility {
			if c.Database == b.Name {
				if found || !CompatibleObservation(b.Name, ObserveSchema(ctx, b, definitions), c) {
					return false
				}
				found = true
			}
		}
		if !found {
			return false
		}
	}
	return true
}
