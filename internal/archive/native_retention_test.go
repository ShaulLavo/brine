package archive

import (
	"strings"
	"testing"
	"time"

	"github.com/ShaulLavo/brine/internal/data"
)

func TestNativeRetentionRequiresFreshOriginalPointReceipt(t *testing.T) {
	p, native := nativeArchiveFixture(t)
	at := p.CreatedAt.Add(time.Second)
	coverage := NativeCoverage{DatabaseID: native.Binding.DatabaseID, Head: native.Head, Point: data.EngineEvidenceRef{Kind: data.EnginePreparedPointEvidence, ID: strings.Repeat("e", 32), SHA256: strings.Repeat("a", 64), DatabaseID: native.Binding.DatabaseID, BindingID: native.Binding.BindingID, EpochID: native.Head.EpochID}, FenceID: data.FenceID(strings.Repeat("e", 32)), FenceOwnerOpID: "original-prepare", SourceWitnessSHA256: strings.Repeat("b", 64), CheckpointSHA256: strings.Repeat("c", 64), RestoreReceiptSHA256: strings.Repeat("d", 64), StopProofSHA256: strings.Repeat("e", 64), WritersQuiescedAt: at, VerifiedAt: at, ExpiresAt: at.Add(5 * time.Minute)}
	m := Manifest{Plan: p, OperationID: strings.Repeat("f", 32), Coverage: []Coverage{NativeCoverageVariant(coverage)}, QuiescedAt: at, PreparedAt: at}
	d := Disposition{ArchiveID: p.ArchiveID, Sequence: 5, Stage: Retired, CommittedAt: at, RetainUntil: at.Add(30 * 24 * time.Hour)}
	proof := RetentionProof{PolicyHash: p.PolicyHash, TargetHash: p.TargetHash, ArchiveID: p.ArchiveID, VerifiedAt: at, ValidUntil: at.Add(time.Minute), RetainUntil: d.RetainUntil}
	if RetentionProofMatches(proof, p, d, m, at) {
		t.Fatal("native retention admitted without a fresh replay receipt")
	}
	receipt := NativeRetentionReceipt{ID: strings.Repeat("d", 32), SHA256: strings.Repeat("c", 64), ArchiveID: p.ArchiveID, BindingID: native.Binding.BindingID, EpochID: native.Head.EpochID, PointID: coverage.Point.ID, PointSHA256: coverage.Point.SHA256, VerifiedAt: at}
	proof.NativeReceipts = []NativeRetentionReceipt{receipt}
	if !RetentionProofMatches(proof, p, d, m, at) {
		t.Fatal("matching structural replay projection refused")
	}
	for _, mutate := range []func(*NativeRetentionReceipt){
		func(r *NativeRetentionReceipt) { r.ArchiveID = data.ArchiveID(strings.Repeat("0", 32)) },
		func(r *NativeRetentionReceipt) { r.BindingID = data.ReplicaBindingID(strings.Repeat("0", 32)) },
		func(r *NativeRetentionReceipt) { r.EpochID = data.ReplicaEpochID(strings.Repeat("0", 32)) },
		func(r *NativeRetentionReceipt) { r.PointID = strings.Repeat("0", 32) },
		func(r *NativeRetentionReceipt) { r.VerifiedAt = at.Add(-6 * time.Minute) },
		func(r *NativeRetentionReceipt) { r.VerifiedAt = at.Add(time.Second) },
	} {
		changed := receipt
		mutate(&changed)
		proof.NativeReceipts = []NativeRetentionReceipt{changed}
		if RetentionProofMatches(proof, p, d, m, at) {
			t.Fatal("crossed or stale replay projection admitted")
		}
	}
}
