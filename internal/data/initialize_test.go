package data

import (
	"context"
	"os"
	"testing"
)

func TestInitializeReviewedSchemaOnAllocatedEmpty(t *testing.T) {
	b, _ := schemaFixture(t)
	receipt, err := CaptureAllocation(b)
	if err != nil {
		t.Fatal(err)
	}
	definition := SchemaDefinition{Database: b.Name, Marker: "v1", CatalogSHA256: "688d95e9133c228079e32bcbdad7325064146b7b1be403a7bbe4a8b83a9c4134"}
	artifact := SchemaInitializer{Definition: definition, Statements: []string{"CREATE TABLE t(x TEXT)"}}
	if err := InitializeSchema(context.Background(), b, artifact, &receipt); err != nil {
		t.Fatal(err)
	}
	observation := ObserveSchema(context.Background(), b, []SchemaDefinition{definition})
	if observation.State != VerifiedSchema || observation.Marker != "v1" {
		t.Fatalf("schema not initialized: %+v", observation)
	}
	if err := InitializeSchema(context.Background(), b, artifact, &receipt); err == nil {
		t.Fatal("initializer replayed")
	}
}

func TestInitializeSchemaRefusesWithoutEffects(t *testing.T) {
	for _, state := range []string{"missing-receipt", "zero-byte", "non-empty", "hash-mismatch"} {
		t.Run(state, func(t *testing.T) {
			b, p := schemaFixture(t)
			r, err := CaptureAllocation(b)
			if err != nil {
				t.Fatal(err)
			}
			receipt := &r
			switch state {
			case "missing-receipt":
				receipt = nil
			case "zero-byte":
				if err := os.WriteFile(p, nil, 0600); err != nil {
					t.Fatal(err)
				}
			case "non-empty":
				db := createSchema(t, p, "CREATE TABLE existing(x)")
				if err := db.Close(); err != nil {
					t.Fatal(err)
				}
			}
			artifact := SchemaInitializer{Definition: SchemaDefinition{Database: b.Name, Marker: "v1", CatalogSHA256: "688d95e9133c228079e32bcbdad7325064146b7b1be403a7bbe4a8b83a9c4134"}, Statements: []string{"CREATE TABLE t(x TEXT)"}}
			if state == "hash-mismatch" {
				artifact.Definition.CatalogSHA256 = EmptyCatalogSHA256
			}
			if err := InitializeSchema(context.Background(), b, artifact, receipt); err == nil {
				t.Fatal("unsafe initialization accepted")
			}
			if state == "missing-receipt" || state == "hash-mismatch" {
				if _, err := os.Stat(p); !os.IsNotExist(err) {
					t.Fatal("refusal allocated database")
				}
			}
		})
	}
}
