//go:build linux

package host

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/ShaulLavo/brine/internal/apps"
	"github.com/ShaulLavo/brine/internal/archive"
	"github.com/ShaulLavo/brine/internal/dispatch"
	"github.com/ShaulLavo/brine/internal/jobs"
	"github.com/ShaulLavo/brine/internal/ops"
	"github.com/ShaulLavo/brine/internal/store"
	"github.com/ShaulLavo/brine/internal/systemd"
)

func TestSQLiteRetireResolverRequestReplay(t *testing.T) {
	for _, disposition := range []string{"continue", "abandon"} {
		t.Run(disposition, func(t *testing.T) { testRemovalResolverRequestReplay(t, disposition) })
	}
}

func newRemovalResolverReplayRig(t *testing.T, disposition string) (*connectedRemovalRig, apps.ConfigPlan, jobs.Accepted) {
	t.Helper()
	source := newUnclaimedRemovalHostRigSource(t, false, false)
	stage := archive.Quiesced
	if disposition == "continue" {
		stage = archive.Prepared
	}
	source.service.Journal = removalInterrupt{Store: source.prep.state, stage: stage}
	r := connectRemovalRig(t, source)
	p := r.plan(t)
	original := r.apply(t, p)
	if err := r.runtime.Runner.Run(t.Context(), original.OperationID); err == nil {
		t.Fatal("interruption missed")
	}
	return r, p, original
}

func testRemovalResolverRequestReplay(t *testing.T, disposition string) {
	t.Helper()
	r, p, original := newRemovalResolverReplayRig(t, disposition)
	args := dispatch.SQLiteRetireResolveArgs{OperationID: original.OperationID, Disposition: disposition, IdempotencyKey: "resolver-request-replay"}
	accepted := r.call(t, "sqlite_retire_resolve", args).Data.(jobs.Accepted)
	checkRemovalRequestReplay(t, r, "sqlite_retire_resolve", args, accepted)
	if err := r.runtime.Runner.Run(t.Context(), accepted.OperationID); err != nil {
		t.Fatal(err)
	}
	r.checkResolved(t, original.OperationID, accepted.OperationID, disposition)
	checkRemovalRequestReplay(t, r, "sqlite_retire_resolve", args, accepted)
	crossed := args
	crossed.Disposition = "continue"
	if disposition == "continue" {
		crossed.Disposition = "abandon"
	}
	checkRemovalRequestRefused(t, r, "sqlite_retire_resolve", crossed)
	checkRemovalRequestRefused(t, r, "apply", dispatch.ApplyArgs{PlanID: p.PlanID, IdempotencyKey: args.IdempotencyKey})
	args.IdempotencyKey = "fresh-resolver-after-terminal"
	checkRemovalRequestRefused(t, r, "sqlite_retire_resolve", args)
	r.source.noWriterEffects(t)
}

func checkRemovalRequestReplay(t *testing.T, r *connectedRemovalRig, op string, args any, accepted jobs.Accepted) {
	t.Helper()
	db := removalControlDB(t, r.source.prep.root)
	before := removalRowCount(t, db, "SELECT count(*) FROM operations")
	replay := r.call(t, op, args).Data.(jobs.Accepted)
	if replay != accepted || removalRowCount(t, db, "SELECT count(*) FROM operations") != before {
		t.Fatal("replay changed operation identity or count", replay)
	}
}

func checkRemovalRequestRefused(t *testing.T, r *connectedRemovalRig, op string, args any) {
	t.Helper()
	db := removalControlDB(t, r.source.prep.root)
	before := removalRowCount(t, db, "SELECT count(*) FROM operations")
	reply, err := r.response(t, op, args)
	if err == nil || reply.OK || removalRowCount(t, db, "SELECT count(*) FROM operations") != before {
		t.Fatal("crossed or unavailable request accepted", reply, err)
	}
}

type removalFailedLauncher struct{ calls int }

func (l *removalFailedLauncher) Launch(context.Context, systemd.OperationID) error {
	l.calls++
	return errors.New("injected known launch failure")
}

func TestSQLiteRetireKnownLaunchFailureReadable(t *testing.T) {
	for _, resolver := range []bool{false, true} {
		name := "retire"
		if resolver {
			name = "resolver"
		}
		t.Run(name, func(t *testing.T) { testRemovalKnownLaunchFailureReadable(t, resolver) })
	}
}

func removalLaunchFailureRequest(t *testing.T, resolver bool) (*connectedRemovalRig, string, any, func()) {
	t.Helper()
	if resolver {
		return removalResolverLaunchFailureRequest(t)
	}
	r := connectedRemovalVariant(t, "empty")
	p := r.plan(t)
	args := dispatch.ApplyArgs{PlanID: p.PlanID, IdempotencyKey: "failed-retire-launch"}
	later := func() {
		task := r.apply(t, p)
		if err := r.runtime.Runner.Run(t.Context(), task.OperationID); err != nil {
			t.Fatal(err)
		}
		if r.status(t, task.OperationID).Operation.State != ops.Succeeded {
			t.Fatal("later task did not succeed")
		}
	}
	return r, "apply", args, later
}

func testRemovalKnownLaunchFailureReadable(t *testing.T, resolver bool) {
	t.Helper()
	r, request, args, later := removalLaunchFailureRequest(t, resolver)
	original := r.runtime.Jobs.(sqliteRetireOperations)
	adapter := original
	launch := &removalFailedLauncher{}
	adapter.jobs.Launcher = launch
	r.runtime.Jobs, r.runtime.SQLiteRetire = adapter, adapter
	reply, err := r.response(t, request, args)
	if err == nil || reply.OK || launch.calls != 1 {
		t.Fatal("known launch fault missed", reply, err)
	}
	accepted := r.call(t, request, args).Data.(jobs.Accepted)
	status := r.status(t, accepted.OperationID)
	if status.Operation.State != ops.Failed || status.Outcome != nil || len(status.Events) != 3 {
		t.Fatal("launch failure was not preserved without a handler receipt", status)
	}
	if launch.calls != 1 {
		t.Fatal("request replay relaunched failed task")
	}
	r.source.noWriterEffects(t)
	r.runtime.Jobs, r.runtime.SQLiteRetire = original, original
	later()
	checkRemovalRequestReplay(t, r, request, args, accepted)
	if r.status(t, accepted.OperationID).Operation.State != ops.Failed {
		t.Fatal("later claim hid failed launch")
	}
}

func TestSQLiteRetireMissingOutcomeIsNotLaunchFailure(t *testing.T) {
	r := connectedRemovalVariant(t, "empty")
	task := r.apply(t, r.plan(t))
	if err := r.source.prep.state.TransitionOperation(t.Context(), task.OperationID, ops.Queued, ops.Failed); err != nil {
		t.Fatal(err)
	}
	reply, err := r.response(t, "operation", dispatch.OperationArgs{OperationID: task.OperationID})
	if err == nil || reply.OK {
		t.Fatal("arbitrary missing terminal outcome became known launch failure", reply)
	}
	r.source.noWriterEffects(t)
}

func removalResolverLaunchFailureRequest(t *testing.T) (*connectedRemovalRig, string, any, func()) {
	t.Helper()
	r, _, original := newRemovalResolverReplayRig(t, "abandon")
	args := dispatch.SQLiteRetireResolveArgs{OperationID: original.OperationID, Disposition: "abandon", IdempotencyKey: "failed-resolver-launch"}
	later := func() {
		task := r.resolve(t, original.OperationID, "abandon")
		if err := r.runtime.Runner.Run(t.Context(), task.OperationID); err != nil {
			t.Fatal(err)
		}
		r.checkResolved(t, original.OperationID, task.OperationID, "abandon")
	}
	return r, "sqlite_retire_resolve", args, later
}

func TestSQLiteRetireReplayRefusesCrossedIdentity(t *testing.T) {
	r, _, original := newRemovalResolverReplayRig(t, "abandon")
	args := dispatch.SQLiteRetireResolveArgs{OperationID: original.OperationID, Disposition: "abandon", IdempotencyKey: "exact-replay-input"}
	accepted := r.call(t, "sqlite_retire_resolve", args).Data.(jobs.Accepted)
	in, err := r.source.prep.state.ReadSQLiteRetireInput(t.Context(), accepted.OperationID)
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"app", "plan", "original", "disposition", "kind"} {
		checkRemovalReplayInputRefused(t, r, in, args.IdempotencyKey, field)
	}
	if _, found, err := r.source.prep.state.FindSQLiteRetireOperation(t.Context(), in, "foreign-requester", args.IdempotencyKey); err != nil || found {
		t.Fatal("replay leaked across requesters", found, err)
	}
	if _, found, err := r.source.prep.state.FindSQLiteRetireOperation(t.Context(), in, sqlitePrepRequester, "unused-key"); err != nil || found {
		t.Fatal("new key replayed another task", found, err)
	}
}

func checkRemovalReplayInputRefused(t *testing.T, r *connectedRemovalRig, in store.SQLiteRetireInput, key, field string) {
	t.Helper()
	switch field {
	case "app":
		in.App = "other"
	case "plan":
		in.PlanID = "sha256:" + strings.Repeat("f", 64)
	case "original":
		in.OriginalOperationID = "other-original"
	case "disposition":
		in.Disposition = "continue"
	case "kind":
		in.Kind, in.Disposition, in.OriginalOperationID = ops.SQLiteRetire, "", ""
	default:
		t.Fatal("unknown fixture field")
	}
	if _, found, err := r.source.prep.state.FindSQLiteRetireOperation(t.Context(), in, sqlitePrepRequester, key); !errors.Is(err, store.ErrConflict) || found {
		t.Fatal("crossed input replayed", field, found, err)
	}
}

func TestSQLiteRetireLaunchFailureRequiresExactEvents(t *testing.T) {
	r, request, args, _ := removalLaunchFailureRequest(t, false)
	adapter := r.runtime.Jobs.(sqliteRetireOperations)
	adapter.jobs.Launcher = &removalFailedLauncher{}
	r.runtime.Jobs, r.runtime.SQLiteRetire = adapter, adapter
	if reply, err := r.response(t, request, args); err == nil || reply.OK {
		t.Fatal("launch did not fail")
	}
	accepted := r.call(t, request, args).Data.(jobs.Accepted)
	if r.status(t, accepted.OperationID).Operation.State != ops.Failed {
		t.Fatal("known failure unreadable")
	}
	if _, err := r.source.prep.state.AppendEvent(t.Context(), accepted.OperationID, ops.Event{Kind: "failure", Payload: []byte(`{"code":"launch_failed"}`)}); err != nil {
		t.Fatal(err)
	}
	reply, err := r.response(t, "operation", dispatch.OperationArgs{OperationID: accepted.OperationID})
	if err == nil || reply.OK {
		t.Fatal("extra event admitted as exact launcher failure", reply)
	}
}
