package policy

import (
	"bytes"
	"reflect"
	"testing"

	"github.com/ShaulLavo/brine/internal/data"
)

func TestDesiredAppPersistenceDetached(t *testing.T) {
	d := Desired{SchemaVersion: 1, Runtime: &data.RuntimeIdentity{UID: 10001, GID: 10002}, Databases: []data.Database{{Name: "main", PersistentRoot: "/srv/data", MountPath: "/data", Filename: "app.db", BackupDestination: "primary"}}, SchemaCompatibility: []data.SchemaCompatibility{{Database: "main", Accepts: []string{"v2", "v1"}, Startup: "preserve"}}, SchemaDefinitions: []data.SchemaDefinition{{Database: "main", Marker: "v1", CatalogSHA256: data.EmptyCatalogSHA256}}}
	before, err := d.CanonicalBytes()
	if err != nil {
		t.Fatal(err)
	}
	a := d.App()
	if !reflect.DeepEqual(a.Databases, d.Databases) || a.Runtime.UID != d.Runtime.UID || a.SchemaCompatibility[0].Startup != "preserve" {
		t.Fatal("persistence conversion lost input")
	}
	a.Runtime.UID = 42
	a.Databases[0].Filename = "changed.db"
	a.SchemaCompatibility[0].Accepts[0] = "changed"
	a.SchemaDefinitions[0].Marker = "changed"
	after, err := d.CanonicalBytes()
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("conversion aliases desired input")
	}
	if d.SchemaCompatibility[0].Accepts[0] != "v2" {
		t.Fatal("canonical sort mutated caller")
	}
	if d.Stateless() {
		t.Fatal("persistent input classified stateless")
	}
	empty := Desired{SchemaVersion: 1}
	if !empty.Stateless() {
		t.Fatal("ordinary v1 input not stateless")
	}
	for _, candidate := range []Desired{{SchemaVersion: 1, Runtime: d.Runtime}, {SchemaVersion: 1, Databases: d.Databases}, {SchemaVersion: 1, SchemaCompatibility: d.SchemaCompatibility}, {SchemaVersion: 1, SchemaDefinitions: d.SchemaDefinitions}} {
		if candidate.Stateless() {
			t.Fatal("partial declaration classified stateless")
		}
	}
}
func TestNormalizeRetainsRuntimeDeclaration(t *testing.T) {
	p, err := Parse(fixture(t))
	if err != nil {
		t.Fatal(err)
	}
	a := app(t)
	a.Runtime = &data.RuntimeIdentity{UID: 10001, GID: 10001}
	d, err := Normalize(a, p)
	if err != nil || d.Runtime == nil || d.Runtime.UID != a.Runtime.UID || d.Stateless() {
		t.Fatalf("runtime declaration silently discarded: %v", err)
	}
}
