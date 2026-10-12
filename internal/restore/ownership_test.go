package restore

import (
	"context"
	"strings"
	"testing"
	"time"
)

func ownedSources() []SQLiteSource {
	return []SQLiteSource{
		{Kind: SQLiteSnapshot, Snapshot: &SnapshotSource{BindingID: "b1", Epoch: "e1", PointID: "p1", ObjectKey: "epochs/e1/restore-points/p1/snapshot.sqlite", SHA256: strings.Repeat("a", 64), Size: 4096}},
		{Kind: LitestreamLTX, LTX: &LTXSource{BindingID: "b1", Epoch: "e1", TXID: 7, Barrier: &BarrierReceipt{BindingID: "b1", Epoch: "e1", TXID: 7, ReplicaTXID: 7, ObservedAt: time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC), Succeeded: true}}},
	}
}

func TestEngineVerificationOwnsEveryMutableInput(t *testing.T) {
	_, input, _ := setup(t, fixture(t, fixtureSQL))
	input.AcceptedSchemas = []SQLiteSchemaObservation{input.ExpectedSchema}
	input.ExpectedSchema = SQLiteSchemaObservation{}
	input.Invariants = []Invariant{{Kind: RowCount, Table: "fixture_commits", Count: 2}}
	input.Sentinel = &Sentinel{Table: "fixture_commits", Sequence: 7, Marker: "marker-7", CommittedAt: time.Now().UTC()}
	input.Coverage = &CoverageEvidence{Source: cloneSQLiteSource(input.Source), CoveredThrough: time.Now().Add(-time.Minute).UTC()}
	verification, err := SQLiteVerification(input)
	if err != nil {
		t.Fatal(err)
	}
	input.Source.Snapshot.BindingID = "other"
	input.AcceptedSchemas[0].Marker = "other"
	input.Invariants[0].Count = 1
	input.Sentinel.Marker = "other"
	input.Coverage.Source.Snapshot.BindingID = "other"
	if verification.sqlite.Source.Snapshot.BindingID != "b1" || verification.sqlite.AcceptedSchemas[0].Marker == "other" || verification.sqlite.Invariants[0].Count != 2 || verification.sqlite.Sentinel.Marker != "marker-7" || verification.sqlite.Coverage.Source.Snapshot.BindingID != "b1" {
		t.Fatal("verification retained caller-owned evidence")
	}
}

func TestEngineVerificationKeepsTheConstructedInvariant(t *testing.T) {
	engine, input, _ := setup(t, fixture(t, fixtureSQL))
	input.Invariants = []Invariant{{Kind: RowCount, Table: "fixture_commits", Count: 2}}
	verification, err := SQLiteVerification(input)
	if err != nil {
		t.Fatal(err)
	}
	input.Invariants[0].Count = 1
	if _, err = (Verifier{SQLite: engine}).Test(context.Background(), verification); err == nil {
		t.Fatal("caller weakened the retained invariant after construction")
	}
}

func TestEngineVerifiedReceiptGetterOwnsNestedEvidence(t *testing.T) {
	source := ownedSources()[1]
	verified := VerifiedReceipt{kind: "sqlite", sqlite: SQLiteReceipt{Source: source, Barrier: source.LTX.Barrier, Sentinel: &Sentinel{Marker: "kept"}, Coverage: &CoverageEvidence{Source: cloneSQLiteSource(source)}}}
	projected, ok := verified.SQLite()
	if !ok {
		t.Fatal("missing SQLite receipt")
	}
	projected.Source.LTX.Barrier.ReplicaTXID = 9
	projected.Barrier.ReplicaTXID = 9
	projected.Coverage.Source.LTX.Barrier.ReplicaTXID = 9
	projected.Sentinel.Marker = "changed"
	again, _ := verified.SQLite()
	if again.Source.LTX.Barrier.ReplicaTXID != 7 || again.Barrier.ReplicaTXID != 7 || again.Coverage.Source.LTX.Barrier.ReplicaTXID != 7 || again.Sentinel.Marker != "kept" {
		t.Fatal("receipt projection exposed retained pointer evidence")
	}
}
