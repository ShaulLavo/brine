package store

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"time"

	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/policy"
	"github.com/ShaulLavo/brine/internal/sqliteprep"
)

// sqliteRetainIndefinitely represents verified no-object-expiration lifecycle
// evidence. Brine never deletes these objects; freshness still bounds admission.
var sqliteRetainIndefinitely = time.Date(9999, 12, 31, 0, 0, 0, 0, time.UTC)

// sqliteCurrent is the protected current scope of one SQLite database. Every
// field comes from store records: the active incarnation, committed replica,
// latest policy admission, current release declarations, schema registry,
// exact credential delivery and fences. Nothing is accepted from a caller.
type sqliteCurrent struct {
	scope     sqliteprep.Scope
	permit    ReplicaPermit
	held      *data.QuiescenceFence
	accepted  map[string]string
	allocated *data.AllocationReceipt
	expires   time.Time
	// authorization holds current-policy, retention or credential failures. A
	// new preparation requires them; abandon depends only on local source facts
	// and must not be stranded by a later policy admission or expiry.
	authorization error
}

func sqliteHash(raw []byte) string {
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func sqliteRefuse(code sqliteprep.Code) error { return &sqliteprep.Refusal{Code: code} }

func readSQLiteCurrent(ctx context.Context, tx *sql.Tx, app string, database data.DatabaseID, now time.Time) (sqliteCurrent, error) {
	if err := checkSQLiteTaskDatabase(ctx, tx, app, database); err != nil {
		return sqliteCurrent{}, err
	}
	permit, err := readReplicaPermit(ctx, tx, database)
	if err != nil {
		return sqliteCurrent{}, err
	}
	// Pre-release, uncommitted and allocated-empty sources have no protected
	// release declaration or retention record here; they refuse honestly.
	if !permit.Replica.Committed || permit.Replica.CredentialVersion == 0 {
		return sqliteCurrent{}, sqliteRefuse(sqliteprep.InvalidEvidence)
	}
	c := sqliteCurrent{permit: permit, expires: now.Add(sqliteprep.PreparationBudget + time.Minute)}
	c.scope = sqliteprep.Scope{App: app, DatabaseName: permit.Database.Name, IncarnationID: permit.Database.IncarnationID, DatabaseID: database, BindingID: permit.Replica.BindingID, EpochID: permit.Replica.EpochID, ReplicaCommitted: true, ConfigSHA256: permit.Replica.ConfigSHA256, CredentialVersion: permit.Replica.CredentialVersion, ActivatedCredentialVersion: permit.Replica.CredentialVersion, RemotePrefix: strings.TrimSuffix(permit.Replica.RemotePrefix, "/") + "/"}
	destination, err := json.Marshal(permit.Replica.Destination)
	if err != nil {
		return sqliteCurrent{}, ErrInvalid
	}
	c.scope.TargetSHA256 = sqliteHash(destination)
	for _, f := range permit.Fences {
		if f.State == data.FenceHeld {
			held := f
			c.held = &held
		}
	}
	if err = c.readPolicy(ctx, tx); err != nil {
		return sqliteCurrent{}, err
	}
	if err = c.readDeclarations(ctx, tx, now); err != nil {
		return sqliteCurrent{}, err
	}
	c.authorization = errors.Join(c.authorization, c.readCredential(ctx, tx))
	c.allocated, err = readPreparationEvidence(ctx, tx, database)
	return c, err
}

func (c *sqliteCurrent) readPolicy(ctx context.Context, tx *sql.Tx) error {
	var err error
	c.scope.PolicySHA256, err = readSQLitePolicy(ctx, tx, c.scope.DatabaseID)
	return err
}

// readSQLitePolicy reads the latest admitted operator policy for a database.
func readSQLitePolicy(ctx context.Context, q dataQuerier, database data.DatabaseID) (string, error) {
	var hash string
	if err := q.QueryRowContext(ctx, "SELECT policy_hash FROM data_admissions WHERE database_id=? ORDER BY version DESC LIMIT 1", database).Scan(&hash); err != nil {
		return "", err
	}
	hash = strings.TrimPrefix(hash, "sha256:")
	if !data.ValidCatalogHash(hash) {
		return "", &IntegrityError{}
	}
	return hash, nil
}

// readDeclarations binds the current committed release's declared database,
// startup compatibility, registered accepted schemas, restore invariants and
// lifecycle retention evidence. The release must belong to the current policy.
func (c *sqliteCurrent) readDeclarations(ctx context.Context, tx *sql.Tx, now time.Time) error {
	var releaseID string
	err := tx.QueryRowContext(ctx, "SELECT h.current_id FROM release_heads h JOIN data_active_incarnations a ON a.app=h.app WHERE h.app=? AND a.incarnation_id=?", c.scope.App, c.scope.IncarnationID).Scan(&releaseID)
	if errors.Is(err, sql.ErrNoRows) {
		return sqliteRefuse(sqliteprep.InvalidEvidence)
	}
	if err != nil {
		return err
	}
	release, err := readRelease(ctx, tx, c.scope.App, releaseID)
	if err != nil {
		return err
	}
	_, desired, err := loadPlan(ctx, tx, release.PlanID)
	if err != nil {
		return err
	}
	if string(desired.Name) != c.scope.App {
		return sqliteRefuse(sqliteprep.InvalidEvidence)
	}
	if strings.TrimPrefix(desired.PolicyHash, "sha256:") != c.scope.PolicySHA256 {
		c.authorization = sqliteRefuse(sqliteprep.InvalidEvidence)
	}
	declared, err := c.declaration(ctx, tx, desired)
	if err != nil {
		return err
	}
	raw, err := json.Marshal(declared)
	if err != nil {
		return ErrInvalid
	}
	c.scope.DeclarationSHA256 = sqliteHash(raw)
	c.scope.Checks = declared.Checks
	c.authorization = errors.Join(c.authorization, c.retention(desired, now))
	return nil
}

type sqliteDeclaration struct {
	Database      data.SQLiteDatabase            `json:"database"`
	Compatibility data.SQLiteSchemaCompatibility `json:"compatibility"`
	Definitions   []data.SQLiteSchemaDefinition  `json:"definitions"`
	Checks        []data.RestoreInvariant        `json:"checks"`
}

func (c *sqliteCurrent) declaration(ctx context.Context, tx *sql.Tx, desired policy.Desired) (sqliteDeclaration, error) {
	out := sqliteDeclaration{Definitions: []data.SQLiteSchemaDefinition{}, Checks: []data.RestoreInvariant{}}
	found, compatible := 0, 0
	for _, d := range desired.Databases {
		if d.Name == c.scope.DatabaseName {
			out.Database, found = d, found+1
		}
	}
	for _, compat := range desired.SchemaCompatibility {
		if compat.Database == c.scope.DatabaseName {
			out.Compatibility, compatible = compat, compatible+1
		}
	}
	for _, check := range desired.RestoreInvariants {
		if check.Database == c.scope.DatabaseName {
			out.Checks = append(out.Checks, check)
		}
	}
	if found != 1 || compatible != 1 || len(out.Compatibility.Accepts) == 0 || out.Database.BackupDestination != c.permit.Replica.Destination.Reference || out.Database.PersistentRoot != c.permit.Database.Root || out.Database.MountPath != c.permit.Database.MountPath || out.Database.Filename != c.permit.Database.Filename {
		return out, sqliteRefuse(sqliteprep.InvalidEvidence)
	}
	c.accepted = map[string]string{}
	for _, marker := range slices.Sorted(slices.Values(out.Compatibility.Accepts)) {
		var catalog string
		if err := tx.QueryRowContext(ctx, "SELECT catalog_sha256 FROM data_schema_definitions WHERE database_id=? AND marker=?", c.scope.DatabaseID, marker).Scan(&catalog); err != nil {
			return out, errors.Join(sqliteRefuse(sqliteprep.InvalidEvidence), err)
		}
		definition, err := declaredSQLiteSchema(desired.SchemaDefinitions, c.scope.DatabaseName, marker, catalog)
		if err != nil {
			return out, err
		}
		c.accepted[marker] = catalog
		out.Definitions = append(out.Definitions, definition)
	}
	return out, nil
}

func declaredSQLiteSchema(definitions []data.SQLiteSchemaDefinition, database data.DatabaseName, marker, catalog string) (data.SQLiteSchemaDefinition, error) {
	var selected data.SQLiteSchemaDefinition
	found := 0
	for _, definition := range definitions {
		if definition.Database == database && definition.Marker == marker {
			selected, found = definition, found+1
		}
	}
	if found != 1 || selected.CatalogSHA256 != catalog {
		return selected, sqliteRefuse(sqliteprep.InvalidEvidence)
	}
	return selected, nil
}

func (c *sqliteCurrent) retention(desired policy.Desired, now time.Time) error {
	destination := c.permit.Replica.Destination
	for _, e := range desired.BackupRetention {
		if e.Destination != destination.Reference {
			continue
		}
		at, err := time.Parse(time.RFC3339Nano, e.VerifiedAt)
		if !e.Valid() || err != nil || !e.NoObjectExpiration || e.Endpoint != destination.Endpoint || e.Bucket != destination.Bucket || e.BasePrefix != destination.BasePrefix || at.After(now) {
			return sqliteRefuse(sqliteprep.InvalidEvidence)
		}
		raw, err := json.Marshal(e)
		if err != nil {
			return ErrInvalid
		}
		at = at.UTC()
		c.scope.Retention = sqliteprep.Retention{EvidenceSHA256: sqliteHash(raw), ObservedAt: at, FreshUntil: at.Add(time.Duration(e.FreshnessSeconds) * time.Second), RetainUntil: sqliteRetainIndefinitely} //nolint:gosec // Valid bounds freshness to at most 30 days.
		return nil
	}
	return sqliteRefuse(sqliteprep.InvalidEvidence)
}

// readCredential binds the exact activated delivered version, never the newest.
func (c *sqliteCurrent) readCredential(ctx context.Context, tx *sql.Tx) error {
	var raw []byte
	var id string
	err := tx.QueryRowContext(ctx, "SELECT id,canonical FROM data_credential_records WHERE binding_id=? AND kind='receipt' AND json_extract(canonical,'$.version')=?", c.scope.BindingID, c.scope.CredentialVersion).Scan(&id, &raw)
	if errors.Is(err, sql.ErrNoRows) {
		return sqliteRefuse(sqliteprep.InvalidEvidence)
	}
	if err != nil {
		return err
	}
	var record CredentialRecord
	if len(raw) > 8192 || json.Unmarshal(raw, &record) != nil || record.ID != id || !data.ValidID(id) || record.App != c.scope.App || record.Destination != c.permit.Replica.Destination.Reference || record.Kind != "receipt" || record.BindingID != c.scope.BindingID || record.EpochID != c.scope.EpochID || record.Version != c.scope.CredentialVersion || record.CredentialRef != c.permit.Replica.Destination.CredentialRef || record.ReceivedAt.IsZero() {
		return &IntegrityError{}
	}
	canonical, err := json.Marshal(record)
	if err != nil || !bytes.Equal(raw, canonical) {
		return &IntegrityError{}
	}
	// A non-expiring delivery is represented as covering the bounded budget.
	if record.ExpiresAt != nil {
		c.expires = record.ExpiresAt.UTC()
	}
	return nil
}

// schema selects the identical accepted schema. Without a lineage exactly one
// registered accepted marker is admissible; an ambiguous set refuses.
func (c sqliteCurrent) schema(previous *sqliteprep.Scope, now time.Time) (data.SQLiteSchemaObservation, error) {
	if previous != nil {
		s := previous.Schema
		if catalog, ok := c.accepted[s.Marker]; !ok || catalog != s.CatalogSHA256 {
			return data.SQLiteSchemaObservation{}, sqliteRefuse(sqliteprep.InvalidEvidence)
		}
		s.ObservedAt = now
		return s, nil
	}
	if len(c.accepted) != 1 {
		return data.SQLiteSchemaObservation{}, sqliteRefuse(sqliteprep.InvalidEvidence)
	}
	for marker, catalog := range c.accepted {
		state := data.VerifiedSchema
		if marker == data.EmptyMarker {
			state = data.VerifiedEmpty
		}
		return data.SQLiteSchemaObservation{State: state, DatabaseID: c.scope.DatabaseID, ObservedAt: now, Marker: marker, CatalogSHA256: catalog}, nil
	}
	return data.SQLiteSchemaObservation{}, sqliteRefuse(sqliteprep.InvalidEvidence)
}

// location admits a host-observed pre-stop directory only when it matches the
// protected allocation evidence and runner ownership. It grants nothing alone.
func (c sqliteCurrent) location(observed sqliteprep.Location, uid, gid uint32) error {
	a := c.allocated
	if a == nil || observed.Validate() != nil || observed.Device != a.DirectoryDevice || observed.Inode != a.DirectoryInode || observed.UID != uid || observed.GID != gid {
		return sqliteRefuse(sqliteprep.LocalUncertain)
	}
	return nil
}

// sameSQLiteCurrent compares a recorded scope with a re-derived one. Only the
// schema observation time may differ; it is not schema identity.
func sameSQLiteCurrent(recorded, current sqliteprep.Scope) bool {
	current.Schema.ObservedAt = recorded.Schema.ObservedAt
	a, errA := json.Marshal(recorded)
	b, errB := json.Marshal(current)
	return errA == nil && errB == nil && string(a) == string(b)
}
