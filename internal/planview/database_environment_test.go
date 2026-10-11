package planview

import (
	"bytes"
	"strings"
	"testing"

	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/plan"
	"github.com/ShaulLavo/brine/internal/policy"
	"github.com/ShaulLavo/brine/internal/ui"
)

func TestDatabaseEnvironmentVisibleWithoutUserValues(t *testing.T) {
	for _, kind := range []plan.Kind{plan.Create, plan.NoOp, plan.Conflict} {
		p := example(kind)
		p.DatabaseEnvironment = []policy.Environment{{Name: "BRINE_DB_MAIN_PATH", Value: "/data/app.db"}, {Name: "BRINE_DB_MAIN_ENGINE", Value: "sqlite"}, {Name: "BRINE_DB_MAIN_ACCEPTS", Value: "v1,v2"}}
		raw, err := JSON(p)
		if err != nil {
			t.Fatal(err)
		}
		human := Human(p, ui.NewTheme(true), 80)
		for _, e := range p.DatabaseEnvironment {
			if !bytes.Contains(raw, []byte(e.Name)) || !bytes.Contains(raw, []byte(e.Value)) || !strings.Contains(human, e.Name+"="+e.Value) {
				t.Fatalf("missing database environment %s", e.Name)
			}
		}
		if bytes.Contains(raw, []byte("SYNTHETIC_PRIVATE_VALUE")) || strings.Contains(human, "SYNTHETIC_PRIVATE_VALUE") {
			t.Fatal("user environment value exposed")
		}
	}
}

func TestSchemaArtifactDigestVisible(t *testing.T) {
	p := example(plan.Create)
	p.SchemaArtifacts = []data.SchemaArtifact{{Definition: data.SQLiteSchemaDefinition{Database: "main", Marker: "v1", CatalogSHA256: data.EmptyCatalogSHA256}, Digest: "sha256:" + data.EmptyCatalogSHA256}}
	raw, err := JSON(p)
	if err != nil {
		t.Fatal(err)
	}
	human := Human(p, ui.NewTheme(true), 80)
	if !bytes.Contains(raw, []byte(p.SchemaArtifacts[0].Digest)) || !strings.Contains(human, p.SchemaArtifacts[0].Digest) {
		t.Fatalf("initializer digest hidden: %s %s", raw, human)
	}
}
