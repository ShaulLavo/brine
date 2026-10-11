package policy

import (
	"bytes"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/ShaulLavo/brine/internal/spacetimedb"
	"github.com/ShaulLavo/brine/internal/spec"
)

func managedInputs(t testing.TB, frontend bool) (spec.App, Policy) {
	t.Helper()
	raw, err := os.ReadFile("../spec/testdata/managed.toml")
	if err != nil {
		t.Fatal(err)
	}
	if frontend {
		raw = []byte("image=\"registry.example.com:5000/team/web@sha256:" + strings.Repeat("a", 64) + "\"\ncontainer_port=8080\nresources={memory_mb=128,pids_limit=128}\n" + string(raw))
	}
	a, err := spec.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	raw, err = os.ReadFile("testdata/managed-operator.toml")
	if err != nil {
		t.Fatal(err)
	}
	p, err := Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	return a, p
}

func TestManagedNormalizedEnvironmentAndHash(t *testing.T) {
	a, p := managedInputs(t, false)
	d, err := Normalize(a, p)
	if err != nil {
		t.Fatal(err)
	}
	want := []Environment{{Name: "BRINE_DB_MAIN_ACCEPTS", Value: "game-v1,game-v2"}, {Name: "BRINE_DB_MAIN_ENGINE", Value: "spacetimedb"}, {Name: "BRINE_DB_MAIN_NAME", Value: "game"}, {Name: "BRINE_DB_MAIN_URL", Value: "https://other.example.net/_brine/db/main/"}}
	if !reflect.DeepEqual(d.DatabaseEnvironmentValues(), want) || d.Stateless() || d.App().HasAppContainer() {
		t.Fatal("managed environment or engine-only shape differs")
	}
	before, err := d.CanonicalBytes()
	if err != nil {
		t.Fatal(err)
	}
	d.DatabaseEnvironment = []Environment{{Name: "BRINE_DB_MAIN_URL", Value: "https://forged.example.com/"}}
	regenerated, err := d.CanonicalBytes()
	if err != nil || !bytes.Equal(before, regenerated) {
		t.Fatal("generated environment override accepted", err)
	}
	for _, change := range []string{"database", "alias", "accepts", "module", "metadata", "license", "cadence", "domain"} {
		t.Run(change, func(t *testing.T) {
			next, err := Normalize(a, p)
			if err != nil {
				t.Fatal(err)
			}
			switch change {
			case "database":
				next.SpacetimeDB.Database = "other"
			case "alias":
				next.SpacetimeDB.Name = "other"
				next.SchemaCompatibility[0].Database = "other"
			case "accepts":
				next.SpacetimeDB.Module.Accepts = []string{"game-v1"}
				next.SchemaCompatibility[0].Accepts = []string{"game-v1"}
			case "module":
				next.SpacetimeDB.Module.File = "module/other.wasm"
			case "metadata":
				next.SpacetimeDB.Module.Metadata = &spec.ModuleMetadata{WasmSHA256: spec.WasmSHA256(strings.Repeat("a", 64)), RuntimeModuleHash: spec.RuntimeModuleHash(strings.Repeat("b", 64)), Canonicalizer: spacetimedb.CanonicalizerVersion, SchemaSHA256: strings.Repeat("c", 64)}
			case "license":
				next.SpacetimePolicy.LicenseAuthority = "another-grant"
			case "cadence":
				next.SpacetimePolicy.CaptureIntervalSeconds++
			case "domain":
				next.Domains = []spec.Domain{"other.example.net"}
			}
			after, err := next.CanonicalBytes()
			if err != nil || bytes.Equal(before, after) {
				t.Fatal("reviewed input absent from desired hash", err)
			}
		})
	}
}

func TestManagedPolicyRefusesMissingAndCrossedFacts(t *testing.T) {
	a, p := managedInputs(t, true)
	if _, err := Normalize(a, p); err != nil {
		t.Fatal(err)
	}
	cases := []func(*spec.App){
		func(a *spec.App) { a.SpacetimeDB.PersistentRoot = "/foreign" },
		func(a *spec.App) { a.SpacetimeDB.BackupDestination = "foreign" },
		func(a *spec.App) { a.SpacetimeDB.Publisher = "foreign" },
		func(a *spec.App) { a.Secrets["OWNER_TOKEN"] = a.SpacetimeDB.Publisher },
		func(a *spec.App) { a.SpacetimeDB.Resources.MemoryMB = 512 },
		func(a *spec.App) { a.SpacetimeDB.Module.Accepts = []string{"game-v1"} },
	}
	for _, mutate := range cases {
		next := a
		next.SpacetimeDB = a.SpacetimeDB.Clone()
		next.Secrets = map[string]spec.SecretReference{}
		mutate(&next)
		if _, err := Normalize(next, p); err == nil {
			t.Fatal("crossed or denied managed input accepted")
		}
	}
	raw, err := os.ReadFile("testdata/managed-operator.toml")
	if err != nil {
		t.Fatal(err)
	}
	withoutReview := strings.Split(string(raw), "[spacetimedb]")[0]
	missing, err := Parse([]byte(withoutReview))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Normalize(a, missing); err == nil {
		t.Fatal("missing license/backup review accepted")
	}
	for _, attack := range []string{"license_use = \"unknown\"", "capture_interval_seconds = 0", "LicenseUse = \"fixture\"", "command = \"shell\""} {
		candidate := string(raw) + "\n" + attack + "\n"
		if _, err := Parse([]byte(candidate)); err == nil {
			t.Fatal("unsafe review facts accepted")
		}
	}
}

func TestManagedFrontendDefaultsShareOperatorCeilings(t *testing.T) {
	a, p := managedInputs(t, true)
	a.Resources = nil
	d, err := Normalize(a, p)
	if err != nil {
		t.Fatal("frontend defaults exhausted resources before an explicit request", err)
	}
	if d.Resources.MemoryMB+d.SpacetimeDB.Resources.MemoryMB != p.config.Resources.MemoryMB || d.Resources.PIDsLimit+d.SpacetimeDB.Resources.PIDsLimit != p.config.Resources.PIDsLimit {
		t.Fatal("frontend default did not use the remaining operator ceilings")
	}
	a.SpacetimeDB.Resources.MemoryMB = p.config.Resources.MemoryMB
	if _, err := Normalize(a, p); err == nil {
		t.Fatal("frontend admitted without any memory budget")
	}
}
