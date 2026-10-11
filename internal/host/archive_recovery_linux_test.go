//go:build linux

package host

import (
	"strings"
	"testing"
	"time"

	"github.com/ShaulLavo/brine/internal/archive"
	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/restore"
)

func TestArchiveRecoveryReceiptRequiresFreshExactRemoteCoverage(t *testing.T) {
	now := time.Now().UTC()
	cutoff := now.Add(-time.Hour)
	source := restore.RestoreSource{Kind: restore.SQLiteSnapshot, Snapshot: &restore.SnapshotSource{BindingID: strings.Repeat("a", 32), Epoch: strings.Repeat("b", 32), PointID: strings.Repeat("c", 32), ObjectKey: "brine/restore-points/point/snapshot.sqlite", SHA256: strings.Repeat("a", 64), Size: 4096}}
	original := restore.Receipt{OperationID: strings.Repeat("a", 32), Source: source, ToolVersion: restore.SnapshotToolVersion, ObservedAt: cutoff.Add(time.Second), Schema: restore.SchemaObservation{State: restore.VerifiedEmpty, Marker: data.EmptyMarker, CatalogSHA256: data.EmptyCatalogSHA256}, IntegrityCheck: "passed", ForeignKeyCheck: "passed", InvariantCheck: "passed", Coverage: &restore.CoverageEvidence{Source: source, CoveredThrough: cutoff}, LossWindow: restore.LossWindow{State: restore.LossBounded, From: cutoff, To: cutoff.Add(time.Second), Reason: "Independent source-bound coverage proof bounds possible loss; later writes may be absent."}}
	coverage := archive.Coverage{Receipt: &original, WritersQuiescedAt: cutoff, Observation: data.SchemaObservation{State: data.VerifiedEmpty, Marker: data.EmptyMarker, CatalogSHA256: data.EmptyCatalogSHA256}}
	fresh := original
	fresh.OperationID = strings.Repeat("d", 32)
	fresh.ObservedAt = now
	fresh.LossWindow.To = now
	if !archiveRecoveryReceiptMatches(coverage, fresh, now) {
		t.Fatal("fresh restore of original point refused")
	}
	if archiveRecoveryReceiptMatches(coverage, original, now) {
		t.Fatal("old verification relabeled fresh")
	}
	for _, failure := range []string{"source", "cutoff", "future", "integrity", "schema", "unbounded"} {
		changed := fresh
		switch failure {
		case "source":
			snapshot := *source.Snapshot
			snapshot.SHA256 = strings.Repeat("b", 64)
			changed.Source.Snapshot = &snapshot
		case "cutoff":
			proof := *fresh.Coverage
			proof.CoveredThrough = now
			changed.Coverage = &proof
		case "future":
			changed.ObservedAt = now.Add(time.Minute)
		case "integrity":
			changed.IntegrityCheck = "unknown"
		case "schema":
			changed.Schema.Marker = "changed"
		case "unbounded":
			changed.LossWindow.State = restore.LossUnknown
		}
		if archiveRecoveryReceiptMatches(coverage, changed, now) {
			t.Fatal("unbound recovery proof admitted", failure)
		}
	}
}
