package plan

import (
	"bytes"
	"os"
	"testing"

	"github.com/ShaulLavo/brine/internal/data"
)

func TestSQLiteEngineGolden(t *testing.T) {
	for _, name := range []string{"allocated", "unknown"} {
		t.Run(name, func(t *testing.T) {
			testSQLiteEngineGolden(t, name)
		})
	}
}

func testSQLiteEngineGolden(t *testing.T, name string) {
	t.Helper()
	in := persistentReady(t)
	if name == "unknown" {
		fact := &(*in.Snapshot.PersistentData.Value)[0]
		fact.Schema = data.SQLiteSchemaObservation{State: data.Unknown, DatabaseID: fact.Database.DatabaseID, ObservedAt: fact.Schema.ObservedAt, UnknownReason: "missing"}
	}
	assertEngineGolden(t, "sqlite-"+name+"-desired", in.Desired.CanonicalBytes)
	p := build(t, in)
	assertEngineGolden(t, "sqlite-"+name+"-plan", p.CanonicalBytes)
}

func assertEngineGolden(t *testing.T, name string, encode func() ([]byte, error)) {
	t.Helper()
	raw, err := encode()
	if err != nil {
		t.Fatal(err)
	}
	file := "testdata/" + name + ".json"
	if os.Getenv("UPDATE_ENGINE_GOLDEN") == "1" {
		if err = os.WriteFile(file, raw, 0600); err != nil {
			t.Fatal(err)
		}
	}
	root, err := os.OpenRoot("testdata")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = root.Close() }()
	want, err := root.ReadFile(name + ".json")
	if err != nil || !bytes.Equal(raw, want) {
		t.Fatalf("SQLite canonical bytes changed: %s: %v", name, err)
	}
}
