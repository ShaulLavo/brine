package data

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestEngineDeclarationsKeepConcreteFields(t *testing.T) {
	sqlite, err := SQLiteDeclaration(SQLiteDatabase{Name: "main", PersistentRoot: "/srv/data", MountPath: "/data", Filename: "app.db", BackupDestination: "primary", SyncInterval: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	spacetime, err := SpacetimeDBDeclaration(SpacetimeDBDatabase{Name: "game", PersistentRoot: "/srv/game", BackupDestination: "primary"})
	if err != nil {
		t.Fatal("SpacetimeDB declaration required SQLite fields", err)
	}
	for _, declaration := range []EngineDeclaration{sqlite, spacetime} {
		raw, err := json.Marshal(declaration)
		if err != nil {
			t.Fatal(err)
		}
		var decoded EngineDeclaration
		if json.Unmarshal(raw, &decoded) != nil || !reflect.DeepEqual(decoded, declaration) {
			t.Fatal("engine declaration lost its concrete type")
		}
	}
	raw, err := json.Marshal(spacetime)
	if err != nil || strings.Contains(string(raw), "filename") || strings.Contains(string(raw), "mount_path") {
		t.Fatal("SpacetimeDB acquired a SQLite file contract", err)
	}
	if _, ok := spacetime.SQLite(); ok {
		t.Fatal("SpacetimeDB declaration converted to SQLite")
	}
	capabilities, err := spacetime.Kind().BackupCapabilities()
	if err != nil || capabilities != (BackupCapabilities{}) {
		t.Fatal("structural declaration admitted unfinished runtime work", err)
	}
}

func TestEngineDeclarationDecoderRejectsForeignAndIncompleteTags(t *testing.T) {
	valid := `{"engine":"spacetimedb","spacetimedb":{"name":"game","persistent_root":"/srv/game","backup_destination":"primary"}}`
	for _, raw := range []string{
		`null`, `{}`, `{"engine":"unknown","spacetimedb":{}}`,
		strings.Replace(valid, `"engine":"spacetimedb"`, `"engine":"sqlite"`, 1),
		strings.Replace(valid, `"engine":"spacetimedb"`, `"engine":"spacetimedb","engine":"sqlite"`, 1),
		strings.Replace(valid, `"engine":`, `"Engine":`, 1),
		strings.Replace(valid, `"spacetimedb":{`, `"spacetimedb":null,"sqlite":{`, 1),
		strings.Replace(valid, `"name":"game"`, `"name":"game","filename":"app.db"`, 1),
		strings.Replace(valid, `"name":"game"`, `"name":null`, 1),
		strings.Replace(valid, `,"backup_destination":"primary"`, ``, 1),
		valid + `{}`,
	} {
		var declaration EngineDeclaration
		if json.Unmarshal([]byte(raw), &declaration) == nil {
			t.Fatal("unchecked engine declaration accepted")
		}
	}
}

func TestEngineObservationRefusesCatalogEvidenceForSpacetimeDB(t *testing.T) {
	at := time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)
	observed, err := SpacetimeDBObservation(SpacetimeDBSchemaObservation{State: VerifiedSchema, DatabaseID: DatabaseID(strings.Repeat("a", 32)), ObservedAt: at, Marker: "game-v1", ModuleIdentity: strings.Repeat("b", 64), SchemaSHA256: strings.Repeat("c", 64)})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(observed)
	if err != nil {
		t.Fatal(err)
	}
	var decoded EngineObservation
	if err = json.Unmarshal(raw, &decoded); err != nil || decoded.Kind() != EngineSpacetimeDB {
		t.Fatal("SpacetimeDB observation did not round-trip", err)
	}
	if _, ok := decoded.SQLite(); ok {
		t.Fatal("SpacetimeDB observation converted to SQLite")
	}
	for _, forged := range []string{
		strings.Replace(string(raw), `"schema_sha256"`, `"catalog_sha256"`, 1),
		strings.Replace(string(raw), `"engine":"spacetimedb"`, `"engine":"sqlite"`, 1),
		strings.Replace(string(raw), `"module_identity":`, `"module_identity":null,"ignored":`, 1),
	} {
		if json.Unmarshal([]byte(forged), &decoded) == nil {
			t.Fatal("foreign or incomplete schema evidence accepted")
		}
	}
}

func TestEngineOperationsRefuseBeforeFilesystemEffects(t *testing.T) {
	ctx := context.Background()
	for _, target := range []ObservationTarget{{}, SpacetimeDBObservationTarget(DatabaseID(strings.Repeat("a", 32)))} {
		if _, err := ObserveEngine(ctx, target, time.Second); err == nil {
			t.Fatal("unimplemented engine observation admitted")
		}
	}
	if _, err := ObserveEngine(ctx, SpacetimeDBObservationTarget("invalid"), time.Second); !errors.Is(err, ErrUnsupportedEngine) {
		t.Fatal("unimplemented engine was inferred from a path or identifier", err)
	}
	for _, target := range []CaptureTarget{{}, SpacetimeDBCaptureTarget()} {
		if _, err := CaptureEngine(ctx, target, time.Second); err == nil {
			t.Fatal("unimplemented engine capture admitted")
		}
	}
	if _, err := CaptureEngine(ctx, SQLiteCaptureTarget(SQLiteCaptureRequest{StagingRoot: t.TempDir()}), 0); err == nil {
		t.Fatal("unbounded capture admitted")
	}
}

func TestStartupIdentityHasNoSQLiteRuntimeRequirement(t *testing.T) {
	p := StartupIdentity{Engine: EngineSpacetimeDB, IncarnationID: AppIncarnationID(strings.Repeat("a", 32)), DatabaseID: DatabaseID(strings.Repeat("b", 32)), BindingID: ReplicaBindingID(strings.Repeat("c", 32)), EpochID: ReplicaEpochID(strings.Repeat("d", 32)), Fence: StartupUnfenced, Ownership: DestinationLocal, SourceSettled: true}
	if !p.Allows(EngineSpacetimeDB) || p.Allows(EngineSQLite) {
		t.Fatal("shared ownership proof conflated engine runtime or identity")
	}
	for _, mutate := range []func(*StartupIdentity){
		func(p *StartupIdentity) { p.Engine = "unknown" },
		func(p *StartupIdentity) { p.Fence = StartupFenceHeld },
		func(p *StartupIdentity) { p.Fence = "" },
		func(p *StartupIdentity) { p.Ownership = DestinationForeign },
		func(p *StartupIdentity) { p.SourceSettled = false },
		func(p *StartupIdentity) { p.EpochID = "" },
	} {
		bad := p
		mutate(&bad)
		if bad.Allows(EngineSpacetimeDB) {
			t.Fatal("unknown startup identity authorized")
		}
	}
}
