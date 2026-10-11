package restore

import (
	"bytes"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"
)

func TestSQLiteReceiptEngineGolden(t *testing.T) {
	at := time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)
	r := SQLiteReceipt{OperationID: "sqlite-restore-fixture", Source: SQLiteSource{Kind: SQLiteSnapshot, Snapshot: &SnapshotSource{BindingID: "b1", Epoch: "e1", PointID: "point1", ObjectKey: "fixture/restore-points/point1/snapshot.sqlite", SHA256: strings.Repeat("a", 64), Size: 4096}}, ToolVersion: SnapshotToolVersion, ObservedAt: at, Schema: SQLiteSchemaObservation{State: VerifiedEmpty, Marker: "brine-empty-v1", CatalogSHA256: "4f53cda18c2baa0c0354bb5f9a3ecbe5ed12ab4d8e11ba873c2f11161202b945"}, LossWindow: LossWindow{State: LossUnknown, Reason: "No independent last-commit coverage proof; asynchronous replication may lose recent writes."}, IntegrityCheck: "passed", ForeignKeyCheck: "passed", InvariantCheck: "passed"}
	if !r.Valid() {
		t.Fatal("invalid SQLite receipt fixture")
	}
	raw, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	file := "testdata/sqlite-engine-receipt.json"
	if os.Getenv("UPDATE_ENGINE_GOLDEN") == "1" {
		if err = os.WriteFile(file, raw, 0600); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(file)
	if err != nil || !bytes.Equal(raw, want) {
		t.Fatal("SQLite receipt bytes changed", err)
	}
	decoded, err := DecodeReceipt(want)
	if err != nil || !decoded.Valid() {
		t.Fatal("historical SQLite receipt no longer decodes", err)
	}
}
