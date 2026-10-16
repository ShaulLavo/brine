//go:build linux

package store

import (
	"strings"
	"testing"

	"github.com/ShaulLavo/brine/internal/archive"
	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/ops"
)

func TestNativeArchiveConsumerUsesPreparedGeneration(t *testing.T) {
	f := newNativeAbandonFixture(t)
	s, ctx, point := f.state, t.Context(), f.point
	head, _, err := s.ReadEngineHead(ctx, point.BindingID)
	if err != nil {
		t.Fatal(err)
	}
	p := archive.Plan{ID: "sha256:" + strings.Repeat("a", 64), ArchiveID: data.ArchiveID(strings.Repeat("b", 32)), Requester: f.operation.Requester, PolicyHash: "sha256:" + point.PolicySHA256, Generation: point.Generation + 10, Candidate: archive.Candidate{App: f.operation.App, Incarnation: point.IncarnationID}}
	o := archive.Operation{ID: strings.Repeat("c", 32), PlanID: p.ID, ArchiveID: p.ArchiveID}
	d := archive.NativeDatabase{Head: head, Binding: data.SpacetimeDBBinding{BindingID: point.BindingID}, Dependencies: []data.EngineEvidenceRef{{Kind: data.EnginePreparedPointEvidence, ID: point.PointID, EpochID: point.EpochID}}}
	abandonExec(t, s.db, "INSERT INTO data_archive_plans VALUES(?,?,?)", p.ID, p.ArchiveID, []byte(`{}`))
	abandonExec(t, s.db, "INSERT INTO data_archive_operations VALUES(?,?,?)", o.ID, p.ID, point.IncarnationID)
	abandonExec(t, s.db, "INSERT INTO operations(id,requester,idempotency_key,state,created_at,updated_at,kind,app,secret_ref) VALUES(?,?,?,'preflight',?,?,?,?,?)", "retire-generation", p.Requester, "retire-generation", timestamp(), timestamp(), ops.NativeRetire, p.Candidate.App, p.ID)
	preparation, err := s.AcquireEnginePreparation(ctx, point.BindingID, point.FenceID, point.FenceOwnerOpID)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = preparation.Close() }()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := s.claimNativeArchiveDatabase(ctx, tx, p, o, d, preparation.guard); err != nil {
		t.Fatal("archive confused control generation with prepared data generation", err)
	}
}
