//go:build linux

package store

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/datapublish"
	"github.com/ShaulLavo/brine/internal/ops"
)

func TestNativeArchiveTransitionClosureRefusesAnUnfinishedRetainedTransition(t *testing.T) {
	f := newNativeTransitionFixture(t)
	ctx := context.Background()
	closed, err := f.store.ReadNativeArchiveTransitionClosure(ctx, f.runtime.BindingID)
	if err != nil || len(closed) != 0 {
		t.Fatal("empty actual transition closure refused", err)
	}
	claimTransitionFixture(t, f)
	if _, err = f.store.ReadNativeArchiveTransitionClosure(ctx, f.runtime.BindingID); err == nil {
		t.Fatal("unfinished transition omitted from archive closure")
	}
	var count int
	if err = f.store.db.QueryRowContext(ctx, "SELECT count(*) FROM data_archive_operations").Scan(&count); err != nil || count != 0 {
		t.Fatal("readonly closure claimed an archive", err)
	}
}

func TestNativeArchiveAcceptsPreservedPublicationWithoutAnEpoch(t *testing.T) {
	f := newNativeTransitionFixture(t)
	intent := seedAbandonedArchivePublication(t, f)
	ctx := t.Context()
	if _, err := f.store.ReadNativeArchiveTransitionClosure(ctx, f.runtime.BindingID); err == nil {
		t.Fatal("unresolved publication admitted")
	}
	resolve := "resolve-abandoned-publication"
	r := intent.record
	proof := []byte(`{"fixture":"source preserved"}`)
	abandonExec(t, f.store.db, "INSERT INTO data_module_publication_proofs VALUES(?,?,?,?,?)", r.PlanID, "source_preservation", publicationPreservationProof, nativePointHash(proof), proof)
	receipt := datapublish.TaskReceipt{Format: "brine-module-publication-task-v1", Kind: string(ops.ModulePublishResolve), OperationID: resolve, ApplyOperationID: r.OperationID, PlanID: r.PlanID, PointID: r.PointID, BindingID: string(r.BindingID), OldEpochID: string(r.SourceEpochID), NewEpochID: string(r.EpochID), CompletionSHA256: nativePointHash(proof), TransitionState: "not_admitted", Outcome: "source_preserved"}
	abandonExec(t, f.store.db, "INSERT INTO operations(id,requester,idempotency_key,state,created_at,updated_at,kind,app,secret_ref,recovery_of) VALUES(?,?,?,'preflight',?,?,?,?,?,?)", resolve, f.input.Common().Scope.Requester, resolve, timestamp(), timestamp(), ops.ModulePublishResolve, "managed", r.PlanID, r.OperationID)
	raw, _ := json.Marshal(receipt)
	if err := f.store.CompleteTask(ctx, resolve, ops.Succeeded, ops.TaskOutcome{Receipt: raw}); err != nil {
		t.Fatal(err)
	}
	closed, err := f.store.ReadNativeArchiveTransitionClosure(ctx, f.runtime.BindingID)
	if err != nil || len(closed) != 0 {
		t.Fatal("resolved publication without epoch blocks archive", err)
	}
	epochs, err := f.store.ReadEngineArchiveBindings(ctx, r.BindingID)
	if err != nil || len(epochs) != 2 {
		t.Fatal("abandoned repository prefix lost from retention", epochs, err)
	}
	seedNativeEpochRepositoryResult(t, f.store, intent)
	if _, err := f.store.ReadNativeArchiveTransitionClosure(ctx, r.BindingID); err != nil {
		t.Fatal("initialized but unused repository still blocks archive", err)
	}
}

func seedAbandonedArchivePublication(t *testing.T, f nativeTransitionFixture) nativeRepositoryIntent {
	t.Helper()
	s, c := f.store, f.input.Common()
	reserved, err := s.ReadReservedEngine(t.Context(), c.Scope.BindingID)
	if err != nil {
		t.Fatal(err)
	}
	consumer := nativePointConsumer{kind: nativePointModulePublish, pointID: c.PreparedPoint.ID, planID: c.Scope.PlanID, applyOperationID: "abandoned-publication", requester: c.Scope.Requester, binding: c.Scope.BindingID, epoch: c.Base.Head().EpochID, generation: c.Scope.Generation, generationID: strings.Repeat("1", 32), policySHA256: strings.TrimPrefix(c.Scope.PolicyHash, "sha256:")}
	raw, err := consumer.canonical()
	if err != nil {
		t.Fatal(err)
	}
	abandonExec(t, s.db, "INSERT INTO operations(id,requester,idempotency_key,state,created_at,updated_at,kind,app,secret_ref) VALUES(?,?,?,'recovery_required',?,?,?,?,?)", consumer.applyOperationID, consumer.requester, consumer.applyOperationID, timestamp(), timestamp(), ops.ModulePublishApply, "managed", consumer.planID)
	abandonExec(t, s.db, "INSERT INTO data_native_preparations VALUES(?,?,?,?,?,?)", consumer.pointID, c.Scope.ApplyOperationID, consumer.binding, consumer.epoch, c.FenceID, []byte(`{}`))
	abandonExec(t, s.db, "INSERT INTO data_native_prepared_points VALUES(?,?,?,?)", consumer.pointID, consumer.binding, consumer.epoch, []byte(`{}`))
	abandonExec(t, s.db, "INSERT INTO data_native_point_consumers VALUES(?,?,?,?,?,?,?,?,?,?,?)", consumer.pointID, consumer.kind, consumer.planID, consumer.applyOperationID, consumer.requester, consumer.binding, consumer.epoch, consumer.generation, consumer.generationID, consumer.policySHA256, raw)
	a := reserved.Archive
	a.EpochID = f.runtime.EpochID
	a.RemotePrefix, err = data.RemotePrefix(a.Destination.BasePrefix, c.Scope.IncarnationID, c.Scope.DatabaseID, a.EpochID)
	if err != nil {
		t.Fatal(err)
	}
	r := nativeRepositoryIntentRecord{Format: "brine-native-repository-intent-v1", ID: c.Scope.ID, OperationID: consumer.applyOperationID, PlanID: consumer.planID, ConsumerSHA256: nativePointHash(raw), PointID: consumer.pointID, BindingID: consumer.binding, SourceEpochID: consumer.epoch, EpochID: a.EpochID, DatabaseID: c.Scope.DatabaseID, IncarnationID: c.Scope.IncarnationID, FenceID: c.FenceID, FenceOwnerOpID: c.FenceOwnerOperationID, RuntimeVersion: c.Base.Head().Version, RuntimeSHA256: c.Base.Head().RuntimeSHA256, PolicySHA256: consumer.policySHA256, Archive: a, ReservedAt: time.Now().UTC()}
	intent := nativeRepositoryIntentValue(r, abandonJSON(t, r))
	abandonExec(t, s.db, "INSERT INTO data_native_repository_intents VALUES(?,?,?,?,?,?,?,?)", r.ID, r.OperationID, r.BindingID, r.EpochID, a.Destination.Endpoint, a.Destination.Bucket, a.RemotePrefix, intent.raw)
	index := publicationReviewBinding{pointID: r.PointID, binding: r.BindingID, requester: consumer.requester, generation: consumer.generation, generationID: consumer.generationID, pointSHA: strings.Repeat("a", 64), runtimeVersion: r.RuntimeVersion, runtimeSHA: r.RuntimeSHA256}
	abandonExec(t, s.db, "INSERT INTO data_module_publication_reviews VALUES(?,?,?,?,?,?,?,?,?,?,?)", r.PlanID, index.pointID, index.binding, index.requester, index.generation, index.generationID, index.pointSHA, index.runtimeVersion, index.runtimeSHA, strings.Repeat("a", 64), []byte(`{}`))
	claim := publicationClaim{PlanID: r.PlanID, PointID: r.PointID, ApplyOpID: r.OperationID, Requester: consumer.requester, BindingID: r.BindingID, OldEpochID: r.SourceEpochID, NewEpochID: r.EpochID, ConsumerSHA: r.ConsumerSHA256, RepositoryIntentID: r.ID, RepositoryIntentSHA: intent.hash}
	abandonExec(t, s.db, "INSERT INTO data_module_publication_claims VALUES(?,?,?,?,?,?,?,?,?,?,?)", claim.PlanID, claim.PointID, claim.ApplyOpID, claim.Requester, claim.BindingID, claim.OldEpochID, claim.NewEpochID, claim.ConsumerSHA, claim.RepositoryIntentID, claim.RepositoryIntentSHA, abandonJSON(t, claim))
	return intent
}
