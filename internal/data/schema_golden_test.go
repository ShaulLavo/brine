package data

import (
	"context"
	"encoding/json"
	"os"
	"testing"
)

func TestClientAndHostSchemaGoldens(t *testing.T) {
	raw, err := os.ReadFile("testdata/schema-goldens.json")
	if err != nil {
		t.Fatal(err)
	}
	var vectors []struct {
		Name          string
		SQL           string
		CatalogSHA256 string `json:"catalog_sha256"`
	}
	if err := json.Unmarshal(raw, &vectors); err != nil {
		t.Fatal(err)
	}
	for _, v := range vectors {
		t.Run(v.Name, func(t *testing.T) {
			a, err := CompileSchemaFile(t.Context(), "main", "", []byte(v.SQL))
			if err != nil || a.Definition.CatalogSHA256 != v.CatalogSHA256 {
				t.Fatalf("client golden: %+v %v", a, err)
			}
			b, _ := schemaFixture(t)
			allocation, err := CaptureAllocation(b)
			if err != nil {
				t.Fatal(err)
			}
			if err := InitializeSchema(context.Background(), b, a, &allocation); err != nil {
				t.Fatal(err)
			}
			observed := ObserveSQLiteSchema(t.Context(), b, []SQLiteSchemaDefinition{a.Definition})
			if observed.State != VerifiedSchema || observed.CatalogSHA256 != v.CatalogSHA256 {
				t.Fatalf("host catalog: %+v", observed)
			}
			a.Definition.CatalogSHA256 = EmptyCatalogSHA256
			if err := a.Validate(t.Context()); err == nil {
				t.Fatal("host accepted catalog mismatch")
			}
		})
	}
}
