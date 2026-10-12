package spec

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/ShaulLavo/brine/internal/spacetimedb"
)

var managedWasm = []byte{0, 'a', 's', 'm', 1, 0, 0, 0}

const managedSchema = `{"sections":[{"Typespace":{"types":[{"U64":[]}]}}]}`

func writeManagedFile(t *testing.T, filename string, raw []byte) {
	t.Helper()
	if err := os.WriteFile(filename, raw, 0600); err != nil {
		t.Fatal(err)
	}
}

func managedFiles(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "module"), 0700); err != nil {
		t.Fatal(err)
	}
	writeManagedFile(t, filepath.Join(root, "module/game.wasm"), managedWasm)
	writeManagedFile(t, filepath.Join(root, "module/schema-v10.json"), []byte(managedSchema))
	return root
}

func parseManagedFiles(t *testing.T, root string, raw string) App {
	t.Helper()
	app, err := ParseFile(t.Context(), filepath.Join(root, "brine.toml"), []byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	return app
}

func TestManagedFilesResolveOnlyDesiredMetadata(t *testing.T) {
	root := managedFiles(t)
	app := parseManagedFiles(t, root, managedSpec(t))
	m := app.SpacetimeDB.Module
	artifact, native, err := spacetimedb.ModuleIdentities(managedWasm)
	if err != nil {
		t.Fatal(err)
	}
	schema, err := spacetimedb.CanonicalSchemaV10([]byte(managedSchema), spacetimedb.DefaultSchemaLimits())
	if err != nil {
		t.Fatal(err)
	}
	want := ModuleMetadata{WasmSHA256: artifact, RuntimeModuleHash: native, Canonicalizer: schema.Version(), SchemaSHA256: schema.SHA256()}
	if m.Metadata == nil || *m.Metadata != want || app.SpacetimeDB.Validate() != nil {
		t.Fatal("resolved metadata differs from shared native/schema algorithms")
	}
	if m.Marker != "game-v1" || !reflect.DeepEqual(m.Accepts, []string{"game-v1", "game-v2"}) || !reflect.DeepEqual(m.Accepts, app.SchemaCompatibility[0].Accepts) {
		t.Fatal("sidecar changed shared app compatibility")
	}
	if len(app.SchemaDefinitions) != 0 || len(app.SchemaArtifacts) != 0 || len(app.Initializers) != 0 || len(app.Databases) != 0 {
		t.Fatal("sidecar became a registered schema or SQLite initializer")
	}
	withoutCompatibility, _, _ := strings.Cut(managedSpec(t), "[[schema_compatibility]]")
	defaults := parseManagedFiles(t, root, withoutCompatibility)
	if !reflect.DeepEqual(defaults.SpacetimeDB.Module.Accepts, []string{"game-v1"}) || defaults.SpacetimeDB.Module.Marker == schema.Marker() {
		t.Fatal("canonical sidecar marker replaced explicit app marker")
	}
}

func TestManagedFilesContentChangesMetadata(t *testing.T) {
	root := managedFiles(t)
	base := *parseManagedFiles(t, root, managedSpec(t)).SpacetimeDB.Module.Metadata
	writeManagedFile(t, filepath.Join(root, "module/game.wasm"), append(managedWasm, 0, 2, 1, 'x'))
	module := *parseManagedFiles(t, root, managedSpec(t)).SpacetimeDB.Module.Metadata
	if module.WasmSHA256 == base.WasmSHA256 || module.RuntimeModuleHash == base.RuntimeModuleHash || module.SchemaSHA256 != base.SchemaSHA256 {
		t.Fatal("changed program did not change both program identities independently of schema")
	}
	writeManagedFile(t, filepath.Join(root, "module/schema-v10.json"), []byte(strings.Replace(managedSchema, "U64", "U32", 1)))
	schema := *parseManagedFiles(t, root, managedSpec(t)).SpacetimeDB.Module.Metadata
	if schema.SchemaSHA256 == module.SchemaSHA256 || schema.WasmSHA256 != module.WasmSHA256 || schema.RuntimeModuleHash != module.RuntimeModuleHash {
		t.Fatal("changed schema did not change its identity independently of program")
	}
}

func managedFileRefusal(t *testing.T, filename string, raw []byte) {
	t.Helper()
	root := managedFiles(t)
	writeManagedFile(t, filepath.Join(root, filename), raw)
	if _, err := ParseFile(t.Context(), filepath.Join(root, "brine.toml"), []byte(managedSpec(t))); err == nil {
		t.Fatal("invalid client artifact accepted")
	}
}

func TestManagedFilesRefuseUnsupportedPayloads(t *testing.T) {
	t.Run("non Wasm", func(t *testing.T) { managedFileRefusal(t, "module/game.wasm", []byte("not a program")) })
	t.Run("unknown schema", func(t *testing.T) {
		managedFileRefusal(t, "module/schema-v10.json", []byte(`{"sections":[{"Future":[]}]}`))
	})
	t.Run("duplicate schema key", func(t *testing.T) {
		managedFileRefusal(t, "module/schema-v10.json", []byte(`{"sections":[],"sections":[]}`))
	})
	t.Run("trailing schema", func(t *testing.T) { managedFileRefusal(t, "module/schema-v10.json", []byte(managedSchema+" {}")) })
	t.Run("oversized schema", func(t *testing.T) {
		managedFileRefusal(t, "module/schema-v10.json", []byte(strings.Repeat(" ", (1<<20)+1)))
	})
	root := managedFiles(t)
	if err := os.Truncate(filepath.Join(root, "module/game.wasm"), spacetimedb.MaxWasmBytes+1); err != nil {
		t.Fatal(err)
	}
	if _, err := ParseFile(t.Context(), filepath.Join(root, "brine.toml"), []byte(managedSpec(t))); err == nil {
		t.Fatal("oversized module accepted")
	}
}

func managedSymlinkRefusal(t *testing.T, filename string) {
	t.Helper()
	root := managedFiles(t)
	original := filepath.Join(root, filename)
	if err := os.Rename(original, original+".real"); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Base(original)+".real", original); err != nil {
		t.Fatal(err)
	}
	if _, err := ParseFile(t.Context(), filepath.Join(root, "brine.toml"), []byte(managedSpec(t))); err == nil {
		t.Fatal("symlink artifact accepted")
	}
}

func TestManagedFilesRefuseSymlinksAndDirectories(t *testing.T) {
	t.Run("module link", func(t *testing.T) { managedSymlinkRefusal(t, "module/game.wasm") })
	t.Run("schema link", func(t *testing.T) { managedSymlinkRefusal(t, "module/schema-v10.json") })
	t.Run("directory link", func(t *testing.T) { managedSymlinkRefusal(t, "module") })
	root := managedFiles(t)
	if err := os.Remove(filepath.Join(root, "module/game.wasm")); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(root, "module/game.wasm"), 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := ParseFile(t.Context(), filepath.Join(root, "brine.toml"), []byte(managedSpec(t))); err == nil {
		t.Fatal("directory artifact accepted")
	}
}

func TestManagedFilesRefusePathEscape(t *testing.T) {
	root := managedFiles(t)
	raw := strings.Replace(managedSpec(t), "module/schema-v10.json", "../schema-v10.json", 1)
	if _, err := ParseFile(t.Context(), filepath.Join(root, "brine.toml"), []byte(raw)); err == nil {
		t.Fatal("escaped schema accepted")
	}
	if _, err := readRelativeFile(root, "../outside", spacetimedb.MaxWasmBytes); err == nil {
		t.Fatal("reader accepted escape before opening")
	}
}
