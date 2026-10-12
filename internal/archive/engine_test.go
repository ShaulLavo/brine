package archive

import (
	"bytes"
	"encoding/json"
	"os"
	"testing"

	"github.com/ShaulLavo/brine/internal/data"
)

func TestSQLiteArchiveDatabaseKeepsRecordedCanonicalBytes(t *testing.T) {
	raw, err := os.ReadFile("testdata/sqlite-database.json")
	if err != nil {
		t.Fatal(err)
	}
	var record Database
	if err = json.Unmarshal(raw, &record); err != nil || record.Engine() != data.EngineSQLite {
		t.Fatal("historical database refused", err)
	}
	encoded, err := json.Marshal(record)
	if err != nil || !bytes.Equal(encoded, raw) {
		t.Fatal("SQLite database canonical bytes changed", err)
	}
	copy, ok := record.SQLite()
	if !ok {
		t.Fatal("SQLite arm absent")
	}
	copy.Allocation.RootInode++
	copy.Epochs[0].RemotePrefix = "caller-changed"
	actual, _ := record.SQLite()
	if actual.Allocation.RootInode == copy.Allocation.RootInode || actual.Epochs[0].RemotePrefix == copy.Epochs[0].RemotePrefix {
		t.Fatal("caller changed retained record")
	}
}

func TestSQLiteArchiveCoverageKeepsRecordedCanonicalBytes(t *testing.T) {
	raw, err := os.ReadFile("testdata/sqlite-coverage.json")
	if err != nil {
		t.Fatal(err)
	}
	var record Coverage
	if err = json.Unmarshal(raw, &record); err != nil || record.Engine() != data.EngineSQLite {
		t.Fatal("historical coverage refused", err)
	}
	encoded, err := json.Marshal(record)
	if err != nil || !bytes.Equal(encoded, raw) {
		t.Fatal("SQLite coverage canonical bytes changed", err)
	}
}

func TestArchiveEngineCodecRejectsMixedAndUnknownArms(t *testing.T) {
	raw, err := os.ReadFile("testdata/sqlite-database.json")
	if err != nil {
		t.Fatal(err)
	}
	var retained Database
	if err = json.Unmarshal(raw, &retained); err != nil {
		t.Fatal(err)
	}
	for _, invalid := range [][]byte{
		append([]byte(`{"engine":"spacetimedb",`), raw[1:]...),
		[]byte(`{"engine":"unknown","spacetimedb":{}}`),
		[]byte(`{"engine":"spacetimedb","spacetimedb":{},"sqlite":{}}`),
		append([]byte(`{"binding":null,`), raw[1:]...),
	} {
		if err = json.Unmarshal(invalid, &retained); err == nil {
			t.Fatal("mixed/unknown/duplicate arm admitted")
		}
		encoded, marshalErr := json.Marshal(retained)
		if marshalErr != nil || !bytes.Equal(encoded, raw) {
			t.Fatal("failed decoder changed prior record", marshalErr)
		}
	}
}
