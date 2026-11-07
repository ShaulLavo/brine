//go:build linux

package enroll

import (
	"context"
	"errors"
	"maps"
	"slices"
	"strings"
	"testing"

	"github.com/ShaulLavo/brine/internal/admission"
	"github.com/ShaulLavo/brine/internal/localexec"
	"github.com/ShaulLavo/brine/internal/workspaceobserve"
)

// All paths and effect calls stay in the repository's injected update fixture.
func TestUpdateCandidateEqualDriftRemainsForwardOnly(t *testing.T) {
	f := newUpdateFixture(t)
	_ = fixtureUpdatePlan(t, f)
	observed := f.write(t, binaryPath, f.h.source, 0755)
	item, err := f.h.planUpdateArtifact(artifactBinary, enrollmentArtifacts()[artifactBinary], "")
	if err != nil || item.Before.SHA256 != observed.SHA256 || item.Before.SHA256 == f.h.r.BinaryHash {
		t.Fatalf("candidate-equal drift contract changed: item=%+v error=%v", item, err)
	}
}

type absentUpdateObserver struct {
	f         *updateFixture
	attempted bool
}

func (e *absentUpdateObserver) Execute(ctx context.Context, c localexec.Command) (localexec.Result, error) {
	if c.Path == "systemd-run" && strings.Contains(strings.Join(c.Args, " "), "restart "+workspaceobserve.UnitName) {
		e.attempted = true
	}
	if c.Path == "systemctl" && len(c.Args) > 1 && c.Args[0] == "show" && c.Args[1] == workspaceobserve.UnitName {
		return localexec.Result{Stdout: "LoadState=not-found\nActiveState=inactive\nSubState=dead\nResult=success\nExecMainStatus=0\nInvocationID=\nMainPID=0\nJob=0\n"}, nil
	}
	return (updateFixtureExecutor{e.f}).Execute(ctx, c)
}
func TestUpdateAbortAddedObserverNoRestart(t *testing.T) {
	f := newUpdateFixture(t)
	plan := fixtureUpdatePlan(t, f)
	if err := f.h.updateEnv.fs.remove(workspaceobserve.UnitPath); err != nil {
		t.Fatal(err)
	}
	delete(f.h.r.Files, workspaceobserve.UnitPath)
	plan.Artifacts = nil
	if err := f.h.planUpdateArtifacts(&plan, ""); err != nil {
		t.Fatal(err)
	}
	plan.Digest = updateDigest(plan)
	f.pending(t, plan)
	// No forward activation was issued. Abort must remove additions and finish.
	e := &absentUpdateObserver{f: f}
	f.h.exec = e
	if err := f.h.stageUpdateArtifacts(); err != nil {
		t.Fatal(err)
	}
	_, err := f.h.continueUpdate(t.Context(), HostRequest{Action: "update-abort", HostKey: plan.HostKey, Confirmed: plan.Digest})
	if err != nil {
		file, _, readErr := f.h.updateEnv.fs.read(workspaceobserve.UnitPath)
		if readErr != nil {
			t.Fatal(readErr)
		}
		t.Fatalf("abort stranded: %v; attempted restart=%t removed observer=%t admission open=%t aborting=%t", err, e.attempted, !file.Present, f.state.Open, f.h.r.Update.Aborting)
	}
	if e.attempted {
		t.Fatal("attempted restart of an observer absent before this transaction")
	}
}

func TestUpdateAbortAddedReconcileNoHandoff(t *testing.T) {
	f := newUpdateFixture(t)
	plan := fixtureUpdatePlan(t, f)
	for _, path := range []string{reconcileUnitPath, reconcileGeneratorPath} {
		if err := f.h.updateEnv.fs.remove(path); err != nil {
			t.Fatal(err)
		}
		delete(f.h.r.Files, path)
	}
	plan.Artifacts = nil
	if err := f.h.planUpdateArtifacts(&plan, ""); err != nil {
		t.Fatal(err)
	}
	plan.ObserverRestart = false
	plan.Digest = updateDigest(plan)
	f.pending(t, plan)
	f.h.exec = absentUpdateReconcile{base: f.h.exec}
	if err := f.h.stageUpdateArtifacts(); err != nil {
		t.Fatal(err)
	}
	_, err := f.h.continueUpdate(t.Context(), HostRequest{Action: "update-abort", HostKey: plan.HostKey, Confirmed: plan.Digest})
	if err != nil {
		t.Fatalf("no-effect abort requires removed reconcile addition: %v; admission open=%t terminal retained=%t", err, f.state.Open, f.h.r.Update != nil)
	}
}

type absentUpdateReconcile struct {
	base localexec.Executor
}

func (e absentUpdateReconcile) Execute(ctx context.Context, c localexec.Command) (localexec.Result, error) {
	if c.Path == "/usr/sbin/runuser" && strings.Contains(strings.Join(c.Args, " "), "--user show brine-reconcile.service") {
		return localexec.Result{ExitCode: 1, Stderr: "Unit brine-reconcile.service could not be found."}, errors.New("unit not found")
	}
	return e.base.Execute(ctx, c)
}

func TestUpdateAbortAddedReconcileCrashContinuation(t *testing.T) {
	for _, mode := range []string{"store", "runner", "bootstrap"} {
		t.Run(mode, func(t *testing.T) { updateReconcileAbortCrashes(t, mode) })
	}
}

func updateReconcileAbortCrashes(t *testing.T, mode string) {
	t.Helper()
	boundaries := []string{"rollback", "terminal-closed", "terminal-open"}
	if mode == "bootstrap" {
		boundaries = append(boundaries, "removal")
	}
	for _, boundary := range boundaries {
		t.Run(boundary, func(t *testing.T) { updateReconcileAbortCrash(t, mode, boundary) })
	}
}

func updateReconcileAbortCrash(t *testing.T, mode, boundary string) {
	t.Helper()
	f, plan := absentReconcileFixture(t, mode)
	f.pending(t, plan)
	f.h.r.Update.PriorIntents = maps.Clone(f.h.r.Journal.Intents)
	if err := f.h.stageUpdateArtifacts(); err != nil {
		t.Fatal(err)
	}
	if mode != "store" {
		prepareFencedAbort(t, f)
	}
	base := f.h.updateEnv.save
	crashed := false
	f.h.updateEnv.save = func(record hostRecord) error {
		if !crashed && reconcileAbortBoundary(record, f.state.Open, boundary) {
			crashed = true
			return errors.New("fixture abort crash")
		}
		return base(record)
	}
	request := HostRequest{Action: "update-abort", HostKey: plan.HostKey, Confirmed: plan.Digest}
	if _, err := f.h.continueUpdate(t.Context(), request); err == nil || !crashed {
		t.Fatal("abort did not reach injected boundary", mode, boundary, err)
	}
	f.restart(t)
	if _, err := f.h.continueUpdate(t.Context(), request); err != nil {
		t.Fatal("abort could not resume", mode, boundary, err)
	}
	if f.h.r.Update != nil {
		t.Fatal("abort retained pending state")
	}
	if mode != "bootstrap" && !f.state.Open {
		t.Fatal("abort left admission closed")
	}
}

func absentReconcileFixture(t *testing.T, mode string) (*updateFixture, UpdatePlan) {
	t.Helper()
	f := newUpdateFixture(t)
	plan := fixtureUpdatePlan(t, f)
	if mode != "store" {
		f, plan = bootstrapUpdateFixture(t)
	}
	if mode == "runner" {
		if err := f.h.installAdmission(t.Context()); err != nil {
			t.Fatal(err)
		}
		plan.Bootstrap = false
	}
	for _, path := range []string{reconcileUnitPath, reconcileGeneratorPath} {
		if err := f.h.updateEnv.fs.remove(path); err != nil {
			t.Fatal(err)
		}
		delete(f.h.r.Files, path)
	}
	plan.Artifacts = nil
	if err := f.h.planUpdateArtifacts(&plan, ""); err != nil {
		t.Fatal(err)
	}
	plan.ObserverRestart = false
	plan.Digest = updateDigest(plan)
	f.h.exec = absentUpdateReconcile{base: f.h.exec}
	return f, plan
}

func prepareFencedAbort(t *testing.T, f *updateFixture) {
	t.Helper()
	if err := f.h.fenceRunner(t.Context()); err != nil {
		t.Fatal(err)
	}
	if f.h.r.Admission == nil {
		if err := f.h.installAdmission(t.Context()); err != nil {
			t.Fatal(err)
		}
	}
}

func reconcileAbortBoundary(record hostRecord, open bool, boundary string) bool {
	u := record.Update
	if u == nil {
		return boundary == "terminal-open" && open
	}
	if boundary == "terminal-closed" {
		return u.Stage == admission.Terminal && !open
	}
	if boundary == "rollback" {
		return u.Aborting && slices.ContainsFunc(u.Artifacts, func(receipt updateArtifactReceipt) bool { return receipt.Undone })
	}
	if boundary == "removal" {
		return u.RemovalPending && slices.Contains(u.RemovedAdmission[:], true)
	}
	return boundary == "terminal-open" && open && u.Reopening != nil
}
