package plan

import (
	"bytes"
	"reflect"
	"testing"

	"github.com/ShaulLavo/brine/internal/policy"
)

func TestDatabaseEnvironmentInPlanAndMountPathHash(t *testing.T) {
	in := persistentReady(t)
	p := build(t, in)
	want := []policy.Environment{{Name: "BRINE_DB_MAIN_ACCEPTS", Value: "brine-empty-v1"}, {Name: "BRINE_DB_MAIN_ENGINE", Value: "sqlite"}, {Name: "BRINE_DB_MAIN_PATH", Value: "/data/app.db"}}
	if p.Kind != Create || !reflect.DeepEqual(p.DatabaseEnvironment, want) {
		t.Fatalf("database environment omitted: %+v", p)
	}
	raw, err := p.CanonicalBytes()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(raw, []byte(`"database_environment"`)) || !bytes.Contains(raw, []byte(`/data/app.db`)) {
		t.Fatal("database environment not serialized")
	}
	in.Desired.Databases[0].MountPath = "/other"
	next := build(t, in)
	if next.DesiredHash == p.DesiredHash || next.Hash == p.Hash || reflect.DeepEqual(next.DatabaseEnvironment, p.DatabaseEnvironment) {
		t.Fatal("mount change did not change plan")
	}
	if next.DatabaseEnvironment[2].Value != "/other/app.db" {
		t.Fatal(next.DatabaseEnvironment)
	}
}
