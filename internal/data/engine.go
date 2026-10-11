package data

import (
	"bytes"
	"encoding/json"
	"errors"
	"time"

	"github.com/ShaulLavo/brine/internal/strictjson"
)

type EngineKind string

const (
	EngineSQLite      EngineKind = "sqlite"
	EngineSpacetimeDB EngineKind = "spacetimedb"
)

var ErrUnsupportedEngine = errors.New("data engine operation is not admitted")

func (k EngineKind) Valid() bool { return k == EngineSQLite || k == EngineSpacetimeDB }

// EngineDeclaration carries one concrete declaration. Its zero value is invalid.
// Existing SQLite canonical records remain concrete and retain their old bytes.
type EngineDeclaration struct {
	kind      EngineKind
	sqlite    SQLiteDatabase
	spacetime SpacetimeDBDatabase
}

// SpacetimeDBDatabase describes an owned engine tree, never a SQLite filename.
// This structural record does not admit deployment or engine mutation.
type SpacetimeDBDatabase struct {
	Name              DatabaseName         `json:"name"`
	PersistentRoot    PersistentRoot       `json:"persistent_root"`
	BackupDestination BackupDestinationRef `json:"backup_destination"`
}

func (d SpacetimeDBDatabase) Validate() error {
	if !namePattern.MatchString(string(d.Name)) || !namePattern.MatchString(string(d.BackupDestination)) || !ValidRoot(string(d.PersistentRoot)) {
		return ErrInvalid
	}
	return nil
}

func SQLiteDeclaration(d SQLiteDatabase) (EngineDeclaration, error) {
	if d.Validate() != nil {
		return EngineDeclaration{}, ErrInvalid
	}
	return EngineDeclaration{kind: EngineSQLite, sqlite: d}, nil
}
func SpacetimeDBDeclaration(d SpacetimeDBDatabase) (EngineDeclaration, error) {
	if d.Validate() != nil {
		return EngineDeclaration{}, ErrInvalid
	}
	return EngineDeclaration{kind: EngineSpacetimeDB, spacetime: d}, nil
}
func (d EngineDeclaration) Kind() EngineKind               { return d.kind }
func (d EngineDeclaration) SQLite() (SQLiteDatabase, bool) { return d.sqlite, d.kind == EngineSQLite }
func (d EngineDeclaration) SpacetimeDB() (SpacetimeDBDatabase, bool) {
	return d.spacetime, d.kind == EngineSpacetimeDB
}
func (d EngineDeclaration) MarshalJSON() ([]byte, error) {
	switch d.kind {
	case EngineSQLite:
		if d.sqlite.Validate() != nil {
			return nil, ErrInvalid
		}
		return engineJSON(d.kind, "sqlite", d.sqlite)
	case EngineSpacetimeDB:
		if d.spacetime.Validate() != nil {
			return nil, ErrInvalid
		}
		return engineJSON(d.kind, "spacetimedb", d.spacetime)
	default:
		return nil, ErrInvalid
	}
}
func (d *EngineDeclaration) UnmarshalJSON(raw []byte) error {
	kind, value, err := engineValue(raw)
	if err != nil {
		return err
	}
	var decoded EngineDeclaration
	switch kind {
	case EngineSQLite:
		if err = engineRecord(value, []string{"name", "persistent_root", "mount_path", "filename", "backup_destination"}, []string{"sync_interval"}); err != nil {
			return err
		}
		concrete, parseErr := strictjson.Value[SQLiteDatabase](value)
		if parseErr != nil {
			return ErrInvalid
		}
		decoded, err = SQLiteDeclaration(concrete)
	case EngineSpacetimeDB:
		if _, err = strictjson.Object(value, "name", "persistent_root", "backup_destination"); err != nil {
			return ErrInvalid
		}
		concrete, parseErr := strictjson.Value[SpacetimeDBDatabase](value)
		if parseErr != nil {
			return ErrInvalid
		}
		decoded, err = SpacetimeDBDeclaration(concrete)
	default:
		return ErrInvalid
	}
	if err != nil {
		return err
	}
	*d = decoded
	return nil
}

// BackupCapabilities describes admitted operations, not upstream possibilities.
// SpacetimeDB capabilities remain false until its runtime/capture gates ship.
type BackupCapabilities struct {
	OnlineCapture        bool `json:"online_capture"`
	ExactFencedCapture   bool `json:"exact_fenced_capture"`
	IsolatedVerification bool `json:"isolated_verification"`
}

func (k EngineKind) BackupCapabilities() (BackupCapabilities, error) {
	switch k {
	case EngineSQLite:
		return BackupCapabilities{OnlineCapture: true, ExactFencedCapture: true, IsolatedVerification: true}, nil
	case EngineSpacetimeDB:
		return BackupCapabilities{}, nil
	default:
		return BackupCapabilities{}, ErrInvalid
	}
}

// EngineObservation keeps catalog evidence confined to SQLite.
type EngineObservation struct {
	kind      EngineKind
	sqlite    SQLiteSchemaObservation
	spacetime SpacetimeDBSchemaObservation
}
type SpacetimeDBSchemaObservation struct {
	State          SchemaState `json:"state"`
	DatabaseID     DatabaseID  `json:"database_id"`
	ObservedAt     time.Time   `json:"observed_at"`
	Marker         string      `json:"marker,omitempty"`
	ModuleIdentity string      `json:"module_identity,omitempty"`
	SchemaSHA256   string      `json:"schema_sha256,omitempty"`
	UnknownReason  string      `json:"unknown_reason,omitempty"`
}

func (o SpacetimeDBSchemaObservation) Validate() error {
	if !ValidID(string(o.DatabaseID)) || o.ObservedAt.IsZero() {
		return ErrInvalid
	}
	switch o.State {
	case Unknown:
		if o.UnknownReason == "" || len(o.UnknownReason) > 128 || o.Marker != "" || o.ModuleIdentity != "" || o.SchemaSHA256 != "" {
			return ErrInvalid
		}
	case AllocatedEmpty, VerifiedEmpty:
		if o.UnknownReason != "" || o.Marker != "" || o.ModuleIdentity != "" || o.SchemaSHA256 != "" {
			return ErrInvalid
		}
	case VerifiedSchema:
		if o.UnknownReason != "" || !ValidMarker(o.Marker) || o.Marker == EmptyMarker || !hashPattern.MatchString(o.ModuleIdentity) || !ValidCatalogHash(o.SchemaSHA256) {
			return ErrInvalid
		}
	default:
		return ErrInvalid
	}
	return nil
}
func SQLiteObservation(o SQLiteSchemaObservation) EngineObservation {
	return EngineObservation{kind: EngineSQLite, sqlite: o}
}
func SpacetimeDBObservation(o SpacetimeDBSchemaObservation) (EngineObservation, error) {
	if o.Validate() != nil {
		return EngineObservation{}, ErrInvalid
	}
	return EngineObservation{kind: EngineSpacetimeDB, spacetime: o}, nil
}
func (o EngineObservation) Kind() EngineKind { return o.kind }
func (o EngineObservation) SQLite() (SQLiteSchemaObservation, bool) {
	return o.sqlite, o.kind == EngineSQLite
}
func (o EngineObservation) SpacetimeDB() (SpacetimeDBSchemaObservation, bool) {
	return o.spacetime, o.kind == EngineSpacetimeDB
}
func (o EngineObservation) MarshalJSON() ([]byte, error) {
	switch o.kind {
	case EngineSQLite:
		if !validSQLiteObservation(o.sqlite) {
			return nil, ErrInvalid
		}
		return engineJSON(o.kind, "sqlite", o.sqlite)
	case EngineSpacetimeDB:
		if o.spacetime.Validate() != nil {
			return nil, ErrInvalid
		}
		return engineJSON(o.kind, "spacetimedb", o.spacetime)
	default:
		return nil, ErrInvalid
	}
}
func (o *EngineObservation) UnmarshalJSON(raw []byte) error {
	kind, value, err := engineValue(raw)
	if err != nil {
		return err
	}
	var decoded EngineObservation
	switch kind {
	case EngineSQLite:
		if err = engineRecord(value, []string{"state", "database_id", "observed_at"}, []string{"marker", "catalog_sha256", "unknown_reason"}); err != nil {
			return err
		}
		concrete, parseErr := strictjson.Value[SQLiteSchemaObservation](value)
		if parseErr != nil || !validSQLiteObservation(concrete) {
			return ErrInvalid
		}
		decoded = SQLiteObservation(concrete)
	case EngineSpacetimeDB:
		if err = engineRecord(value, []string{"state", "database_id", "observed_at"}, []string{"marker", "module_identity", "schema_sha256", "unknown_reason"}); err != nil {
			return err
		}
		concrete, parseErr := strictjson.Value[SpacetimeDBSchemaObservation](value)
		if parseErr != nil {
			return ErrInvalid
		}
		decoded, err = SpacetimeDBObservation(concrete)
	default:
		return ErrInvalid
	}
	if err != nil {
		return err
	}
	*o = decoded
	return nil
}
func validSQLiteObservation(o SQLiteSchemaObservation) bool {
	if !ValidID(string(o.DatabaseID)) || o.ObservedAt.IsZero() {
		return false
	}
	switch o.State {
	case Unknown:
		return o.UnknownReason != "" && o.Marker == "" && o.CatalogSHA256 == ""
	case AllocatedEmpty, VerifiedEmpty:
		return o.UnknownReason == "" && o.Marker == EmptyMarker && o.CatalogSHA256 == EmptyCatalogSHA256
	case VerifiedSchema:
		return o.UnknownReason == "" && ValidMarker(o.Marker) && o.Marker != EmptyMarker && ValidCatalogHash(o.CatalogSHA256)
	default:
		return false
	}
}
func engineJSON(kind EngineKind, key string, value any) ([]byte, error) {
	return json.Marshal(map[string]any{"engine": kind, key: value})
}
func engineValue(raw []byte) (EngineKind, json.RawMessage, error) {
	fields, err := engineRecordFields(raw, []string{"engine"}, []string{"sqlite", "spacetimedb"})
	if err != nil {
		return "", nil, err
	}
	kind, err := strictjson.Value[EngineKind](fields["engine"])
	if err != nil || !kind.Valid() || len(fields) != 2 || fields[string(kind)] == nil {
		return "", nil, ErrInvalid
	}
	return kind, fields[string(kind)], nil
}
func engineRecord(raw []byte, required, optional []string) error {
	_, err := engineRecordFields(raw, required, optional)
	return err
}
func engineRecordFields(raw []byte, required, optional []string) (map[string]json.RawMessage, error) {
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil {
		return nil, ErrInvalid
	}
	names := make([]string, 0, len(fields))
	allowed := make(map[string]bool, len(required)+len(optional))
	for _, name := range required {
		allowed[name] = true
	}
	for _, name := range optional {
		allowed[name] = true
	}
	for name, value := range fields {
		if !allowed[name] || bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			return nil, ErrInvalid
		}
		names = append(names, name)
	}
	for _, name := range required {
		if fields[name] == nil {
			return nil, ErrInvalid
		}
	}
	checked, err := strictjson.Object(raw, names...)
	if err != nil {
		return nil, ErrInvalid
	}
	return checked, nil
}
