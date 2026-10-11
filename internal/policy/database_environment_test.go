package policy

import (
	"bytes"
	"reflect"
	"testing"

	"github.com/ShaulLavo/brine/internal/data"
)

func TestNormalizedDatabaseEnvironment(t *testing.T) {
	p, err := Parse(fixture(t))
	if err != nil {
		t.Fatal(err)
	}
	a := app(t)
	a.Runtime = &data.RuntimeIdentity{UID: 10001, GID: 10001}
	a.Databases = []data.SQLiteDatabase{{Name: "main-db", PersistentRoot: "/srv/data", MountPath: "/data", Filename: "app.db", BackupDestination: "primary"}}
	a.SchemaCompatibility = []data.SQLiteSchemaCompatibility{{Database: "main-db", Accepts: []string{"v2", "v1"}, Startup: "preserve"}}
	d, err := Normalize(a, p)
	if err != nil {
		t.Fatal(err)
	}
	want := []Environment{{Name: "BRINE_DB_MAIN_DB_ACCEPTS", Value: "v1,v2"}, {Name: "BRINE_DB_MAIN_DB_ENGINE", Value: "sqlite"}, {Name: "BRINE_DB_MAIN_DB_PATH", Value: "/data/app.db"}}
	if got := d.DatabaseEnvironmentValues(); !reflect.DeepEqual(got, want) || !reflect.DeepEqual(d.DatabaseEnvironment, want) {
		t.Fatalf("got %+v want %+v", got, want)
	}
	before, err := d.CanonicalBytes()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(before, []byte(`"database_environment"`)) {
		t.Fatal("generated values missing from desired hash material")
	}
	d.DatabaseEnvironment = []Environment{{Name: "BRINE_DB_MAIN_DB_PATH", Value: "/forged.db"}}
	regenerated, err := d.CanonicalBytes()
	if err != nil || !bytes.Equal(before, regenerated) {
		t.Fatal("stored settings overrode derived hash values", err)
	}
	env := d.DatabaseEnvironmentValues()
	env[0].Value = "changed"
	if !reflect.DeepEqual(d.DatabaseEnvironmentValues(), want) {
		t.Fatal("generated environment aliases input")
	}
	for _, change := range []string{"mount", "filename", "accepts", "name"} {
		t.Run(change, func(t *testing.T) {
			next := d.App()
			switch change {
			case "mount":
				next.Databases[0].MountPath = "/other"
			case "filename":
				next.Databases[0].Filename = "other.db"
			case "accepts":
				next.SchemaCompatibility[0].Accepts = []string{"v3"}
			case "name":
				next.Databases[0].Name = "other"
				next.SchemaCompatibility[0].Database = "other"
			}
			changed, err := Normalize(next, p)
			if err != nil {
				t.Fatal(err)
			}
			after, err := changed.CanonicalBytes()
			if err != nil {
				t.Fatal(err)
			}
			if bytes.Equal(before, after) || reflect.DeepEqual(changed.DatabaseEnvironmentValues(), want) {
				t.Fatal("environment change absent from desired hash material")
			}
		})
	}
}
