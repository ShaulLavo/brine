package data

import (
	"context"
	"encoding/json"
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

func TestEngineOperationsRefuseBeforeFilesystemEffects(t *testing.T) {
	ctx := context.Background()
	for _, target := range []ObservationTarget{{}, {kind: EngineSpacetimeDB}} {
		if _, err := ObserveEngine(ctx, target, time.Second); err == nil {
			t.Fatal("unimplemented engine observation admitted")
		}
	}
	for _, target := range []CaptureTarget{{}, {kind: EngineSpacetimeDB}} {
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
