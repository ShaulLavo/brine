package dispatch

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/spacetimedb"
	"github.com/ShaulLavo/brine/internal/spec"
)

func managedMetadataFixture() ManagedModuleInput {
	return ManagedModuleInput{Database: "main", Metadata: spec.ModuleMetadata{
		WasmSHA256: spec.WasmSHA256(strings.Repeat("a", 64)), RuntimeModuleHash: spec.RuntimeModuleHash(strings.Repeat("b", 64)),
		Canonicalizer: spacetimedb.CanonicalizerVersion, SchemaSHA256: strings.Repeat("c", 64),
	}}
}

func TestManagedMetadataCodecRefusesUnknownDuplicateNullAndLimits(t *testing.T) {
	want := managedMetadataFixture()
	raw, err := json.Marshal(want)
	if err != nil {
		t.Fatal(err)
	}
	var decoded ManagedModuleInput
	if json.Unmarshal(raw, &decoded) != nil || decoded != want {
		t.Fatal("metadata round trip failed")
	}
	attacks := []string{
		`null`, strings.Replace(string(raw), `"database":"main"`, `"database":null`, 1),
		strings.Replace(string(raw), `"database":"main"`, `"database":"main","database":"main"`, 1),
		strings.Replace(string(raw), `"metadata":{`, `"metadata":{"unknown":true,`, 1),
		strings.Replace(string(raw), `"wasm_sha256":"`+strings.Repeat("a", 64)+`"`, `"wasm_sha256":null`, 1),
		strings.Replace(string(raw), `"runtime_module_hash":"`+strings.Repeat("b", 64)+`"`, `"runtime_module_hash":"0x`+strings.Repeat("b", 64)+`"`, 1),
		strings.Replace(string(raw), `"schema_sha256":"`+strings.Repeat("c", 64)+`"`, `"schema_sha256":"`+strings.Repeat("C", 64)+`"`, 1),
		strings.Replace(string(raw), `"canonicalizer":"`+spacetimedb.CanonicalizerVersion+`"`, `"canonicalizer":"foreign"`, 1),
		string(raw) + `{}`, strings.Repeat(" ", managedMetadataLimit) + string(raw),
	}
	for _, attack := range attacks {
		if json.Unmarshal([]byte(attack), &decoded) == nil {
			t.Fatal("invalid metadata admitted")
		}
		if decoded != want {
			t.Fatal("failed decoding changed prior metadata")
		}
	}
}

func managedTransportFixture(t *testing.T) (string, spec.App) {
	t.Helper()
	raw, err := os.ReadFile("../spec/testdata/managed.toml")
	if err != nil {
		t.Fatal(err)
	}
	app, err := spec.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	metadata := managedMetadataFixture().Metadata
	app.SpacetimeDB.Module.Metadata = &metadata
	return string(raw), app
}

func TestManagedPlanTransportOwnsMetadataAndCannotChangeAliasOrEngine(t *testing.T) {
	raw, app := managedTransportFixture(t)
	request, err := NewPlanArgs(raw, app)
	if err != nil {
		t.Fatal(err)
	}
	app.SpacetimeDB.Module.Metadata.WasmSHA256 = spec.WasmSHA256(strings.Repeat("d", 64))
	if request.ManagedModule.Metadata.WasmSHA256 != managedMetadataFixture().Metadata.WasmSHA256 {
		t.Fatal("request aliases caller metadata")
	}
	wire, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := decodePlan(wire)
	if err != nil {
		t.Fatal(err)
	}
	got := decoded.(spec.App)
	if got.SpacetimeDB.Module.Marker != "game-v1" || got.SpacetimeDB.Name != "main" || *got.SpacetimeDB.Module.Metadata != request.ManagedModule.Metadata {
		t.Fatal("metadata changed declaration")
	}
	request.ManagedModule.Database = "foreign"
	wire, _ = json.Marshal(request)
	if _, err := decodePlan(wire); err == nil {
		t.Fatal("foreign alias admitted")
	}
	request.ManagedModule.Database = "main"
	request.SchemaArtifacts = []data.SchemaArtifact{{}}
	wire, _ = json.Marshal(request)
	if _, err := decodePlan(wire); err == nil {
		t.Fatal("mixed SQLite artifact admitted")
	}
	request.SchemaArtifacts = []data.SchemaArtifact{}
	request.ManagedModule = nil
	wire, _ = json.Marshal(request)
	if _, err := decodePlan(wire); err == nil {
		t.Fatal("missing managed metadata admitted")
	}
}
