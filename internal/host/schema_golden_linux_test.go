//go:build linux

package host

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/datainit"
)

func TestHostReviewedSchemaGoldens(t *testing.T) {
	raw, err := os.ReadFile("../data/testdata/schema-goldens.json")
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
			a := data.SchemaInitializer{Definition: data.SQLiteSchemaDefinition{Database: "main", Marker: "golden-v1", CatalogSHA256: v.CatalogSHA256}, Statements: strings.Split(strings.TrimSuffix(v.SQL, ";"), ";")}
			for _, mismatch := range []bool{false, true} {
				if mismatch {
					a.Definition.CatalogSHA256 = data.EmptyCatalogSHA256
				}
				evidence, raw, err := a.Artifact()
				if err != nil {
					t.Fatal(err)
				}
				decoded, err := datainit.DecodeArtifact(raw, evidence.Digest)
				if err != nil {
					t.Fatal(err)
				}
				if err := decoded.Validate(t.Context()); (err != nil) != mismatch {
					t.Fatalf("host recompilation mismatch=%v: %v", mismatch, err)
				}
			}
		})
	}
}
