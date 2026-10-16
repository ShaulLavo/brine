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
	s, prepared, _, point := nativeCompletedPointFixture(t)
	ctx := t.Context()
	original, err := s.GetOperation(ctx, prepared.intent.OperationID)
	if err != nil {
		t.Fatal(err)
	}
	head, _, err := s.ReadEngineHead(ctx, point.BindingID)
	if err != nil {
		t.Fatal(err)
	}
	p := archive.Plan{ID: "sha256:" + strings.Repeat("a", 64), ArchiveID: data.ArchiveID(strings.Repeat("b", 32)), Requester: original.Requester, PolicyHash: "sha256:" + point.PolicySHA256, Generation: point.Generation + 10, Candidate: archive.Candidate{App: original.App, Incarnation: point.IncarnationID}}
	o := archive.Operation{ID: strings.Repeat("c", 32), PlanID: p.ID, ArchiveID: p.ArchiveID}
	d := archive.NativeDatabase{Head: head, Binding: data.SpacetimeDBBinding{BindingID: point.BindingID}, Dependencies: []data.EngineEvidenceRef{{Kind: data.EnginePreparedPointEvidence, ID: point.PointID, EpochID: point.EpochID}}}
	abandonExec(t, s.db, "INSERT INTO data_archive_plans VALUES(?,?,?)", p.ID, p.ArchiveID, []byte(`{}`))
	abandonExec(t, s.db, "INSERT INTO data_archive_operations VALUES(?,?,?)", o.ID, p.ID, point.IncarnationID)
	abandonExec(t, s.db, "INSERT INTO operations(id,requester,idempotency_key,state,created_at,updated_at,kind,app,secret_ref) VALUES(?,?,?,'preflight',?,?,?,?,?)", "retire-generation", p.Requester, "retire-generation", timestamp(), timestamp(), ops.NativeRetire, p.Candidate.App, p.ID)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	loaded, err := readNativePreparedPointTx(ctx, tx, point.PointID)
	if err != nil {
		t.Fatal("fixture prepared point", err)
	}
	if err := checkNativePointCurrent(ctx, tx, loaded.view); err != nil {
		t.Fatal("fixture point is not current", err)
	}
	if loaded.view.RuntimeVersion != head.Version || loaded.view.RuntimeSHA256 != head.RuntimeSHA256 || loaded.view.EpochID != head.EpochID || p.PolicyHash != "sha256:"+loaded.view.PolicySHA256 {
		t.Fatal("fixture archive head or policy differs", head, loaded.view.RuntimeVersion, loaded.view.RuntimeSHA256, loaded.view.EpochID, p.PolicyHash, loaded.view.PolicySHA256)
	}
	if _, err := s.claimNativeArchiveDatabase(ctx, tx, p, o, d, prepared.preparation.guard); err != nil {
		t.Fatal("archive confused control generation with prepared data generation", err)
	}
}
