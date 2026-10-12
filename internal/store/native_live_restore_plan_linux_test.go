//go:build linux

package store

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/datainit"
	"github.com/ShaulLavo/brine/internal/liverestore"
)

func storedNativeLiveRestoreFixture(t *testing.T) (nativeTransitionFixture, liverestore.Plan) {
	t.Helper()
	f := newNativeTransitionFixture(t)
	ctx := context.Background()
	head, _, err := f.store.ReadEngineHead(ctx, f.runtime.BindingID)
	if err != nil {
		t.Fatal(err)
	}
	common, hash := f.input.Common(), strings.Repeat("a", 64)
	id, historical := strings.Repeat("a", 32), strings.Repeat("b", 32)
	requester, err := datainit.ParseRequester(common.Scope.Requester)
	if err != nil {
		t.Fatal(err)
	}
	at := time.Now().UTC()
	facts := liverestore.Facts{App: "fixture", IncarnationID: common.Scope.IncarnationID, DatabaseID: common.Scope.DatabaseID, Head: head,
		Generation: common.Scope.Generation, GenerationID: id, PolicySHA256: hash, TargetSHA256: hash, DesiredSHA256: hash}
	safety := liverestore.Safety{PointID: common.PreparedPoint.ID, PointSHA256: common.PreparedPoint.SHA256, FenceID: common.FenceID,
		FenceOwnerOpID: common.FenceOwnerOperationID, SourceSealSHA256: hash, SourceWitnessSHA256: hash, StopProofSHA256: hash,
		CheckpointSHA256: hash, CommittedOffset: 21, CommittedOffsetKnown: true, PreparedAt: at.Add(-time.Minute), ExpiresAt: at.Add(10 * time.Minute)}
	selected := liverestore.SelectedPoint{IncarnationID: facts.IncarnationID, DatabaseID: facts.DatabaseID, BindingID: head.BindingID,
		PointID: historical, PointSHA256: hash, EpochID: head.EpochID, GenerationID: id, CaptureID: historical, RepositoryID: hash,
		RepositorySnapshot: hash, ManifestSHA256: hash, RestoreEvidenceSHA256: hash, CheckpointSHA256: hash, StateWitnessSHA256: hash,
		NativeDatabase: hash, NativePublisher: hash, ProgramWireHash: hash, ArtifactWasmSHA256: hash, SchemaSHA256: hash,
		MaterialSHA256: hash, DependenciesSHA256: hash, CommittedOffset: 8, CapturedAt: at.Add(-time.Hour), RetainUntil: at.Add(30 * 24 * time.Hour)}
	p, err := liverestore.NewPlan(liverestore.PlanSpec{Requester: requester, Facts: facts, Safety: safety, Selected: selected,
		Recovery: liverestore.RecoveryRelease{ReleaseID: "recovery-code", CodeSHA256: hash, CompatibilitySHA256: hash, ProgramWireHash: hash, SchemaSHA256: hash},
		IssuedAt: at, ExpiresAt: at.Add(5 * time.Minute)})
	if err != nil {
		t.Fatal(err)
	}
	return f, p
}

func TestNativeLiveRestoreReviewImmutableAndDoesNotClaimProtection(t *testing.T) {
	f, p := storedNativeLiveRestoreFixture(t)
	ctx := context.Background()
	for range 2 {
		if err := f.store.SaveNativeLiveRestorePlan(ctx, p); err != nil {
			t.Fatal(err)
		}
	}
	stored, err := f.store.LoadNativeLiveRestorePlan(ctx, p.ID())
	if err != nil || stored.ID() != p.ID() || !bytes.Equal(stored.Bytes(), p.Bytes()) {
		t.Fatal("selected review changed in storage", err)
	}
	if _, err = f.store.db.ExecContext(ctx, "UPDATE data_native_live_restore_plans SET selected_point_id=? WHERE id=?", p.Spec().Safety.PointID, p.ID()); err == nil {
		t.Fatal("historical selection changed")
	}
	if _, err = f.store.db.ExecContext(ctx, "DELETE FROM data_native_live_restore_plans WHERE id=?", p.ID()); err == nil {
		t.Fatal("review deletion admitted")
	}
	var claims int
	if err = f.store.db.QueryRowContext(ctx, "SELECT count(*) FROM data_native_live_restore_claims").Scan(&claims); err != nil || claims != 0 {
		t.Fatal("saving review consumed protection", err)
	}
	head, _, err := f.store.ReadEngineHead(ctx, p.Spec().Facts.Head.BindingID)
	if err != nil || head != p.Spec().Facts.Head {
		t.Fatal("saving review moved data head", err)
	}
}

func TestNativeLiveRestoreReaderRejectsIndexedScopeMismatch(t *testing.T) {
	f, p := storedNativeLiveRestoreFixture(t)
	ctx := context.Background()
	spec := p.Spec()
	_, err := f.store.db.ExecContext(ctx, "INSERT INTO data_native_live_restore_plans VALUES(?,?,?,?,?)", p.ID(), spec.Facts.Head.BindingID,
		string(data.FenceID(strings.Repeat("c", 32))), spec.Selected.PointID, p.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.store.LoadNativeLiveRestorePlan(ctx, p.ID()); err == nil {
		t.Fatal("indexed scope mismatch admitted")
	}
	if err = f.store.SaveNativeLiveRestorePlan(ctx, p); err == nil {
		t.Fatal("idempotent save hid corrupted existing review")
	}
}
