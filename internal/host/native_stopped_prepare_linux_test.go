//go:build linux

package host

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/datainit"
	"github.com/ShaulLavo/brine/internal/localexec"
	"github.com/ShaulLavo/brine/internal/ops"
	"github.com/ShaulLavo/brine/internal/podman"
	"github.com/ShaulLavo/brine/internal/result"
	"github.com/ShaulLavo/brine/internal/store"
)

func stoppedUnpublishedPrepareFixture(t *testing.T) (*nativePrepareWork, nativeMaterialFixture) {
	t.Helper()
	f := freshNativeMaterialFixture(t)
	packet := freshNativeMaterialPacket(t)
	defer clear(packet)
	deliverNativeMaterialFixture(t, f, strings.Repeat("1", 32), packet)
	ctx := context.Background()
	requester, err := datainit.ParseRequester(f.service.Requester)
	if err != nil {
		t.Fatal(err)
	}
	input := store.NativeTaskInput{Requester: requester, Kind: ops.NativePrepare, BindingID: f.plan.Managed.Database.BindingID}
	ref, err := f.service.Store.SaveNativeTaskInput(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	op, _, err := f.service.Store.CreateOperation(ctx, ops.Intent{Kind: ops.NativePrepare, App: f.plan.App, SecretRef: ref}, requester.String(), "stopped-empty-prepare")
	if err != nil {
		t.Fatal(err)
	}
	if err := f.service.Store.SetOperationState(ctx, op.ID, ops.Preflight); err != nil {
		t.Fatal(err)
	}
	session, err := localexec.NewSession(nativeMaterialImageExecutor{}, nativeBackupFixtureUID(t), f.home, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	w := &nativePrepareWork{runtime: nativeBackupRuntime{state: f.service.Store, stateRoot: f.stateRoot, home: f.home, session: session}, input: input, operation: op}
	t.Cleanup(func() {
		if err := w.close(ctx); err != nil {
			t.Error(err)
		}
	})
	if err := w.acquire(ctx); err != nil {
		t.Fatal(err)
	}
	if err := f.service.Store.RecordNativeStopIntent(ctx, w.native, store.NativeStopIntent{Mode: "unpublished_absent", ObservedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	w.lifetime, err = data.AcquireEngineLifetimeLock(ctx, f.stateRoot, input.BindingID)
	if err != nil {
		t.Fatal(err)
	}
	w.stopped, err = f.service.Store.RecordNativeStoppedPreparation(ctx, w.native, w.lifetime, store.NativeStopObservation{Mode: "unpublished_absent", FinishedAt: time.Now().UTC(), SourceAbsent: true, RestartJobsAbsent: true, IngressClosed: true})
	if err != nil {
		t.Fatal(err)
	}
	return w, f
}

func TestStoppedUnpublishedPreparationReachesInitialPublisherLease(t *testing.T) {
	w, _ := stoppedUnpublishedPrepareFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var err error
	w.parent, w.path, err = newNativePreparedRestoreParent(ctx, w.permit)
	if err != nil {
		t.Fatal("workspace", err)
	}
	copyID, err := data.NewID()
	if err != nil {
		t.Fatal(err)
	}
	w.copied, err = copyEngineSource(ctx, w.permit.Reserved.Database, w.path, copyID, engineCopyLimits{Bytes: 64 << 20, Files: 16384, Depth: 16})
	if err != nil {
		t.Fatal("copy", err)
	}
	empty, err := buildNativeEmptySource(ctx, w.runtime.state, w.runtime.stateRoot, w.stopped, w.copied)
	if err != nil {
		t.Fatal("empty source", err)
	}
	empty, err = w.sealedRecoveryEmpty(ctx, empty)
	if err != nil {
		t.Fatal("signing recovery", err)
	}
	lease, err := acquireInitialPublisherStage(ctx, empty, podman.New(w.runtime.session))
	if err != nil {
		t.Fatal("initial lease", err)
	}
	defer lease.material.clear()
}

func TestStoppedUnpublishedOriginalInspectionDoesNotRequireUpload(t *testing.T) {
	w, f := stoppedUnpublishedPrepareFixture(t)
	ctx := context.Background()
	routes := store.NativePreparedRoutes{PointID: w.stopped.PointID(), OperationID: w.operation.ID, App: f.plan.App, PreviousFiles: map[string]string{}, CurrentFiles: map[string]string{}}
	if err := f.service.Store.RecordNativePreparedRoutes(ctx, w.native, routes, false); err != nil {
		t.Fatal(err)
	}
	if err := f.service.Store.RecordNativePreparedRoutes(ctx, w.native, routes, true); err != nil {
		t.Fatal(err)
	}
	if err := f.service.Store.CompleteTask(ctx, w.operation.ID, ops.RecoveryRequired, ops.TaskOutcome{Error: result.InternalError}); err != nil {
		t.Fatal(err)
	}
	before, err := f.service.Store.GetOperation(ctx, w.operation.ID)
	if err != nil {
		t.Fatal(err)
	}
	outcome, err := f.service.Store.ReadTaskOutcome(ctx, w.operation.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := w.close(ctx); err != nil {
		t.Fatal(err)
	}
	inspection, err := f.service.Store.OpenNativeBackupInspection(ctx, w.operation.ID, f.service.Requester)
	if err != nil {
		t.Fatal("original stopped claim required nonexistent upload descriptor", err)
	}
	expected, err := inspection.PreparedExpectation(ctx)
	if err != nil || !expected.NeedsCapture || expected.PointID != w.stopped.PointID() || expected.Stop.Mode != "unpublished_absent" {
		t.Fatal("original stopped claim lost original authority", err)
	}
	observation := strings.Repeat("a", 64)
	review, err := inspection.Review(ctx, observation)
	if err != nil {
		t.Fatal(err)
	}
	if err := inspection.Close(); err != nil {
		t.Fatal(err)
	}
	resolver, _, err := f.service.Store.CreateOperation(ctx, ops.Intent{Kind: ops.NativeBackupResolve, App: f.plan.App, SecretRef: review.ID, RecoveryOf: w.operation.ID}, f.service.Requester, "continue-stopped-original")
	if err != nil {
		t.Fatal(err)
	}
	if err := f.service.Store.SetOperationState(ctx, resolver.ID, ops.Preflight); err != nil {
		t.Fatal(err)
	}
	inspection, err = f.service.Store.ClaimNativeBackupResolution(ctx, resolver.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = inspection.Close() }()
	if err := inspection.CheckReviewedObservation(ctx, strings.Repeat("b", 64)); err == nil {
		t.Fatal("changed stage/source observation continued original")
	}
	if err := inspection.CheckReviewedObservation(ctx, observation); err != nil {
		t.Fatal(err)
	}
	life, err := data.AcquireEngineLifetimeLock(ctx, f.stateRoot, w.input.BindingID)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = life.Release() }()
	current := expected.Stop
	current.FinishedAt = time.Now().UTC()
	stopped, err := inspection.ResumePrepared(ctx, life, current)
	if err != nil || stopped.PointID() != w.stopped.PointID() || stopped.OperationID() != w.operation.ID || stopped.FenceOwnerOperationID() != w.fence.OperationID {
		t.Fatal("resolver replaced original stopped capability", err)
	}
	after, err := f.service.Store.GetOperation(ctx, w.operation.ID)
	if err != nil || after != before {
		t.Fatal("resolution rewrote original failed operation", err)
	}
	afterOutcome, err := f.service.Store.ReadTaskOutcome(ctx, w.operation.ID)
	a, _ := json.Marshal(outcome)
	b, _ := json.Marshal(afterOutcome)
	if err != nil || !bytes.Equal(a, b) {
		t.Fatal("resolution rewrote original failure outcome", err)
	}
}

func TestStoppedOriginalWithUnknownInitialStageCannotQualifyForContinuation(t *testing.T) {
	w, f := stoppedUnpublishedPrepareFixture(t)
	ctx := context.Background()
	source, err := openEngineSource(w.permit.Reserved.Database)
	if err != nil {
		t.Fatal(err)
	}
	seal, err := sealOwnedEngineSource(ctx, source, engineCopyLimits{Bytes: 64 << 20, Files: 16384, Depth: 16})
	closeErr := source.Close()
	if err != nil || closeErr != nil {
		t.Fatal(err, closeErr)
	}
	stage, err := f.service.Store.BeginNativeInitialStage(ctx, w.stopped, strings.Repeat("a", 32), strings.Repeat("b", 32), strings.Repeat("c", 32), seal.hash)
	if err != nil {
		t.Fatal(err)
	}
	if err := stage.Record(ctx, store.NativeInitialStageObservation{Stage: "unknown", Code: "workspace_create", At: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	if err := f.service.Store.CompleteTask(ctx, w.operation.ID, ops.RecoveryRequired, ops.TaskOutcome{Error: result.InternalError}); err != nil {
		t.Fatal(err)
	}
	if err := w.close(ctx); err != nil {
		t.Fatal(err)
	}
	inspection, err := f.service.Store.OpenNativeBackupInspection(ctx, w.operation.ID, f.service.Requester)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = inspection.Close() }()
	stages, err := inspection.InitialStages(ctx)
	if err != nil || len(stages) != 1 || stages[0].IsDisposed() {
		t.Fatal("unknown initial-stage provenance lost", err)
	}
	if err := inspection.RequireInitialStagesSettled(ctx); err == nil {
		t.Fatal("unknown initial stage supplied continuation authority")
	}
}
