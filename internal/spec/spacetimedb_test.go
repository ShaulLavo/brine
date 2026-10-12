package spec

import (
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/ShaulLavo/brine/internal/data"
)

func managedSpec(t testing.TB) string {
	t.Helper()
	raw, err := os.ReadFile("testdata/managed.toml")
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func TestManagedDatabaseContract(t *testing.T) {
	a, err := Parse([]byte(managedSpec(t)))
	if err != nil {
		t.Fatal(err)
	}
	if a.HasAppContainer() || a.SpacetimeDB == nil || len(a.Databases) != 0 || a.Runtime != nil {
		t.Fatal("managed engine became an application container or SQLite declaration")
	}
	d := a.SpacetimeDB
	declaration, err := d.Declaration()
	if err != nil || declaration.Kind() != data.EngineSpacetimeDB {
		t.Fatal("missing closed engine declaration", err)
	}
	if d.ClientPrefix() != "/_brine/db/main/" || d.EnvironmentPrefix() != "BRINE_DB_MAIN" || d.SortedAccepts() != "game-v1,game-v2" {
		t.Fatal("connection contract differs")
	}
	if !reflect.DeepEqual(d.Module.Accepts, a.SchemaCompatibility[0].Accepts) {
		t.Fatal("managed accepts did not derive from shared compatibility")
	}
	copy := d.Clone()
	copy.Module.Accepts[0] = "changed"
	if d.Module.Accepts[0] == "changed" {
		t.Fatal("module accepts aliased caller")
	}
}

func TestManagedOptionalFrontend(t *testing.T) {
	frontend := "image=\"registry.example.com:5000/team/web@sha256:" + strings.Repeat("a", 64) + "\"\ncontainer_port=8080\n"
	a, err := Parse([]byte(frontend + managedSpec(t)))
	if err != nil || !a.HasAppContainer() {
		t.Fatal("valid frontend refused", err)
	}
	for _, partial := range []string{strings.Split(frontend, "container_port")[0], "container_port=8080\n"} {
		if _, err := Parse([]byte(partial + managedSpec(t))); err == nil {
			t.Fatal("unpaired frontend accepted")
		}
	}
}

func TestManagedRefusals(t *testing.T) {
	base := managedSpec(t)
	cases := map[string]string{
		"unknown service field":              strings.Replace(base, "version =", "command = \"shell\"\nversion =", 1),
		"case alias":                         strings.Replace(base, "version =", "Version =", 1),
		"SQLite filename":                    strings.Replace(base, "engine =", "filename = \"app.db\"\nengine =", 1),
		"crossed payload":                    strings.Replace(base, "engine = \"spacetimedb\"", "engine = \"sqlite\"", 1),
		"duplicate database":                 base + "\n[[databases]]\nengine=\"spacetimedb\"\nname=\"other\"\n",
		"shared runtime":                     "[runtime]\nuid=1000\ngid=1000\n" + base,
		"bad database":                       strings.Replace(base, "database = \"game\"", "database = \"../game\"", 1),
		"public service accepts":             strings.Replace(base, "version =", "accepts = [\"wrong\"]\nversion =", 1),
		"escaped module":                     strings.Replace(base, "module/game.wasm", "../game.wasm", 1),
		"negative resource":                  strings.Replace(base, "page_pool_mb = 64", "page_pool_mb = -1", 1),
		"resource overflow":                  strings.Replace(base, "memory_mb = 128", "memory_mb = 2147483648", 1),
		"root runtime":                       strings.Replace(base, "uid = 1000", "uid = 0", 1),
		"key reuse":                          strings.Replace(base, "signing-public", "signing-private", 1),
		"private value":                      strings.Replace(base, "publisher = \"publisher\"", "publisher = \"Bearer secret\"", 1),
		"rewrite startup":                    strings.Replace(base, "startup = \"preserve\"", "startup = \"initialize\"", 1),
		"wrong alias":                        strings.Replace(base, "database = \"main\"", "database = \"other\"", 1),
		"marker not accepted":                strings.Replace(base, "accepts = [\"game-v2\", \"game-v1\"]", "accepts = [\"game-v2\"]", 1),
		"frontend settings without frontend": strings.Replace(base, "[[databases]]", "[environment]\nSETTING=\"value\"\n[[databases]]", 1),
	}
	for name, raw := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := Parse([]byte(raw)); err == nil {
				t.Fatal("unsafe managed declaration accepted")
			}
		})
	}
}

func TestManagedRecoveryPasswordReferenceIsExplicitAndSeparate(t *testing.T) {
	raw, err := os.ReadFile("testdata/managed.toml")
	if err != nil {
		t.Fatal(err)
	}
	app, err := Parse(raw)
	if err != nil || app.SpacetimeDB.RecoveryPasswordRef != "recovery-password" {
		t.Fatal("native recovery dependency missing", err)
	}
	for _, changed := range []string{
		strings.Replace(string(raw), "recovery_password_ref = \"recovery-password\"\n", "", 1),
		strings.Replace(string(raw), "recovery_password_ref = \"recovery-password\"", "recovery_password_ref = \"signing-private\"", 1),
		strings.Replace(string(raw), "recovery_password_ref = \"recovery-password\"", "recovery_password_ref = \"auth\"", 1),
		strings.Replace(string(raw), "recovery-password", "Recovery_password", 1),
		strings.Replace(string(raw), "recovery-password", strings.Repeat("a", 64), 1),
	} {
		if _, err := Parse([]byte(changed)); err == nil {
			t.Fatal("missing or reused native recovery dependency admitted")
		}
	}
	sqlite := []byte(strings.Replace(persistentSpec(t), "[[databases]]", "[[databases]]\nrecovery_password_ref=\"recovery-password\"", 1))
	if _, err := Parse(sqlite); err == nil {
		t.Fatal("SQLite accepted native recovery field")
	}
}
