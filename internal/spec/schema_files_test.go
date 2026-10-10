package spec

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func schemaSpec(t *testing.T) []byte {
	t.Helper()
	raw := persistentSpec(t)
	raw, _, _ = strings.Cut(raw, "[[schema_compatibility]]")
	return []byte(raw + "schema=\"db/main.sql\"\n")
}

func TestSchemaFilesRelativeAndDefaultCompatibility(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "db"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "db/main.sql"), []byte("CREATE TABLE t(x TEXT);"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, marker := range []string{"", "orders-v1"} {
		raw := schemaSpec(t)
		if marker != "" {
			raw = append(raw, []byte("marker=\""+marker+"\"\n")...)
		}
		app, err := ParseFile(context.Background(), filepath.Join(root, "brine.toml"), raw)
		if err != nil {
			t.Fatal(err)
		}
		if len(app.Initializers) != 1 || len(app.SchemaArtifacts) != 1 || len(app.SchemaDefinitions) != 1 || len(app.SchemaCompatibility) != 1 {
			t.Fatalf("missing derivation: %+v", app)
		}
		derived := app.SchemaDefinitions[0].Marker
		if (marker != "" && derived != marker) || app.SchemaCompatibility[0].Accepts[0] != derived || app.SchemaCompatibility[0].Startup != "preserve" {
			t.Fatalf("wrong default: %+v", app.SchemaCompatibility)
		}
		host, err := Parse(raw)
		if err != nil {
			t.Fatal(err)
		}
		if err := host.ResolveSchemas(app.SchemaArtifacts); err != nil || host.SchemaDefinitions[0] != app.SchemaDefinitions[0] {
			t.Fatalf("host metadata: %+v %v", host, err)
		}
		app.SchemaArtifacts[0].Definition.Marker = "forged"
		if err := host.ResolveSchemas(app.SchemaArtifacts); err == nil {
			t.Fatal("accepted mismatched marker")
		}
	}
}

func TestSchemaFileBoundaryRefusals(t *testing.T) {
	for _, file := range []string{"", "/tmp/main.sql", "../main.sql", "db/../main.sql", "db\\main.sql"} {
		raw := strings.Replace(string(schemaSpec(t)), "db/main.sql", file, 1)
		if _, err := Parse([]byte(raw)); err == nil {
			t.Fatalf("accepted unsafe schema path %q", file)
		}
	}
	for _, extra := range []string{"\n[[schema_definitions]]\ndatabase=\"main\"\nmarker=\"v1\"\ncatalog_sha256=\"hash\"\n", "catalog_sha256=\"hash\"\n"} {
		if _, err := Parse(append(schemaSpec(t), []byte(extra)...)); err == nil {
			t.Fatal("accepted handwritten catalog identity")
		}
	}
	if _, err := ParseFile(context.Background(), filepath.Join(t.TempDir(), "brine.toml"), schemaSpec(t)); err == nil {
		t.Fatal("accepted missing file")
	}
}

func TestSchemaFileExplicitCompatibilityPreserved(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "db"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "db/main.sql"), []byte("CREATE TABLE t(x TEXT);"), 0600); err != nil {
		t.Fatal(err)
	}
	raw := append(schemaSpec(t), []byte("[[schema_compatibility]]\ndatabase=\"main\"\naccepts=[\"older\"]\nstartup=\"preserve\"\n")...)
	app, err := ParseFile(t.Context(), filepath.Join(root, "brine.toml"), raw)
	if err != nil || app.SchemaCompatibility[0].Accepts[0] != "older" {
		t.Fatalf("overrode explicit compatibility: %v", err)
	}
}

func TestSchemaFileRejectsSymlinks(t *testing.T) {
	for _, kind := range []string{"file", "directory", "loop", "internal-file", "internal-directory"} {
		t.Run(kind, func(t *testing.T) {
			root, outside := t.TempDir(), t.TempDir()
			if err := os.WriteFile(filepath.Join(outside, "main.sql"), []byte("CREATE TABLE t(x TEXT);"), 0600); err != nil {
				t.Fatal(err)
			}
			db := filepath.Join(root, "db")
			if kind == "directory" {
				if err := os.Symlink(outside, db); err != nil {
					t.Fatal(err)
				}
			} else if kind == "internal-directory" {
				if err := os.Mkdir(filepath.Join(root, "real"), 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(root, "real/main.sql"), []byte("CREATE TABLE t(x TEXT);"), 0600); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink("real", db); err != nil {
					t.Fatal(err)
				}
			} else {
				if err := os.Mkdir(db, 0700); err != nil {
					t.Fatal(err)
				}
				target := filepath.Join(outside, "main.sql")
				if kind == "loop" {
					target = "main.sql"
				}
				if kind == "internal-file" {
					target = "real.sql"
					if err := os.WriteFile(filepath.Join(db, target), []byte("CREATE TABLE t(x TEXT);"), 0600); err != nil {
						t.Fatal(err)
					}
				}
				if err := os.Symlink(target, filepath.Join(db, "main.sql")); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := ParseFile(t.Context(), filepath.Join(root, "brine.toml"), schemaSpec(t)); err == nil {
				t.Fatal("accepted schema symlink")
			}
		})
	}
}
