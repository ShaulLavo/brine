package dispatch

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/spec"
)

func TestPlanSchemaMetadataNeverReadsHostPaths(t *testing.T) {
	raw, err := os.ReadFile("../spec/testdata/valid-minimal.toml")
	if err != nil {
		t.Fatal(err)
	}
	raw = append(raw, []byte("\n[runtime]\nuid=10001\ngid=10001\n[[databases]]\nname=\"main\"\npersistent_root=\"/srv/brine-data\"\nmount_path=\"/data\"\nfilename=\"app.db\"\nbackup_destination=\"primary\"\nschema=\"missing-on-host.sql\"\n")...)
	artifact := data.SchemaArtifact{Definition: data.SchemaDefinition{Database: "main", Marker: "sha256:688d95e9133c", CatalogSHA256: "688d95e9133c228079e32bcbdad7325064146b7b1be403a7bbe4a8b83a9c4134"}, Digest: "sha256:" + strings.Repeat("a", 64)}
	wire, err := json.Marshal(PlanArgs{Spec: string(raw), SchemaArtifacts: []data.SchemaArtifact{artifact}})
	if err != nil {
		t.Fatal(err)
	}
	value, err := decodePlan(wire)
	if err != nil {
		t.Fatal(err)
	}
	app := value.(spec.App)
	if app.SchemaDefinitions[0] != artifact.Definition || app.SchemaCompatibility[0].Accepts[0] != artifact.Definition.Marker {
		t.Fatal("metadata not resolved")
	}
	for _, refused := range []string{
		strings.Replace(string(wire), `"digest":`, `"approved":true,"digest":`, 1),
		strings.Replace(string(wire), `"marker":`, `"Marker":`, 1),
		strings.Replace(string(wire), `"catalog_sha256":`, `"sql":"CREATE TABLE t(x)","catalog_sha256":`, 1),
		strings.Replace(string(wire), `sha256:688d95e9133c"`, `sha256:111111111111"`, 1),
	} {
		if _, err := decodePlan([]byte(refused)); err == nil {
			t.Fatal("accepted invalid artifact metadata")
		}
	}
}
