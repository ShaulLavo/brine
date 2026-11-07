//go:build linux

package enroll

import (
	"context"
	"errors"
	"maps"
	"strings"
	"testing"

	"github.com/ShaulLavo/brine/internal/localexec"
	"github.com/ShaulLavo/brine/internal/workspaceobserve"
)

type observerRollbackExecutor struct {
	f             *updateFixture
	disables      int
	presentAtStop bool
}

func (e *observerRollbackExecutor) Execute(ctx context.Context, command localexec.Command) (localexec.Result, error) {
	if command.Path == "systemd-run" && strings.Contains(strings.Join(command.Args, " "), "disable --now "+workspaceobserve.UnitName) {
		e.disables++
		file, _, err := e.f.h.updateEnv.fs.read(workspaceobserve.UnitPath)
		e.presentAtStop = err == nil && file.Present && file.SHA256 == hash([]byte(workspaceobserve.Unit))
	}
	if command.Path == "systemctl" && len(command.Args) > 1 && command.Args[0] == "show" && command.Args[1] == workspaceobserve.UnitName {
		return localexec.Result{Stdout: "LoadState=loaded\nActiveState=inactive\nSubState=dead\nResult=success\nExecMainStatus=0\nInvocationID=stopped\nMainPID=0\nJob=0\n"}, nil
	}
	return (updateFixtureExecutor{e.f}).Execute(ctx, command)
}

func activatedAddedObserver(t *testing.T) (*updateFixture, UpdatePlan, *observerRollbackExecutor) {
	t.Helper()
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
	f.h.r.Update.PriorIntents = maps.Clone(f.h.r.Journal.Intents)
	if err := f.h.stageUpdateArtifacts(); err != nil {
		t.Fatal(err)
	}
	if err := f.h.replaceUpdateArtifacts(t.Context(), fixtureUpdateDrain{f}, false); err != nil {
		t.Fatal(err)
	}
	e := &observerRollbackExecutor{f: f}
	f.h.exec = e
	if err := f.h.issueUpdateEffect(t.Context(), &f.h.r.Update.Activation.Observer, "observer", "/usr/bin/systemctl", "restart", workspaceobserve.UnitName); err != nil {
		t.Fatal("injected native activation did not complete", err)
	}
	return f, plan, e
}

func TestUpdateAbortAddedObserverStopsBeforeRemoval(t *testing.T) {
	f, plan, executor := activatedAddedObserver(t)
	request := HostRequest{Action: "update-abort", HostKey: plan.HostKey, Confirmed: plan.Digest}
	if _, err := f.h.continueUpdate(t.Context(), request); err != nil {
		t.Fatal(err)
	}
	if executor.disables != 1 || !executor.presentAtStop {
		t.Fatal("observer unit removed before checked stop", executor.disables, executor.presentAtStop)
	}
	file, _, err := f.h.updateEnv.fs.read(workspaceobserve.UnitPath)
	if err != nil || file.Present || !f.state.Open || f.h.r.Update != nil {
		t.Fatal("observer addition rollback did not finish", err)
	}
}

func TestUpdateAbortAddedObserverRemovalCrash(t *testing.T) {
	f, plan, executor := activatedAddedObserver(t)
	base := f.h.updateEnv.save
	crashed := false
	f.h.updateEnv.save = func(record hostRecord) error {
		if !crashed && observerRemovedAtSave(f, record) {
			crashed = true
			return errors.New("fixture crash after unit removal")
		}
		return base(record)
	}
	request := HostRequest{Action: "update-abort", HostKey: plan.HostKey, Confirmed: plan.Digest}
	if _, err := f.h.continueUpdate(t.Context(), request); err == nil || !crashed {
		t.Fatal("unit-removal crash did not occur", err)
	}
	f.restart(t)
	if _, err := f.h.continueUpdate(t.Context(), request); err != nil {
		t.Fatal("stopped observer removal could not resume", err)
	}
	if executor.disables != 1 {
		t.Fatal("observer stop was reissued", executor.disables)
	}
}

func observerRemovedAtSave(f *updateFixture, record hostRecord) bool {
	if record.Update == nil || record.Update.RollbackActivation.ObserverRemoval.Observed != "stopped" {
		return false
	}
	file, _, err := f.h.updateEnv.fs.read(workspaceobserve.UnitPath)
	return err == nil && !file.Present
}

func TestUpdateAbortAddedObserverUnknownStopStaysPending(t *testing.T) {
	f, plan, executor := activatedAddedObserver(t)
	f.h.r.Update.RollbackActivation.ObserverRemoval.Intent = true
	if err := f.h.updateSave(); err != nil {
		t.Fatal(err)
	}
	request := HostRequest{Action: "update-abort", HostKey: plan.HostKey, Confirmed: plan.Digest}
	for range 2 {
		if _, err := f.h.continueUpdate(t.Context(), request); err == nil {
			t.Fatal("unknown native stop cleared")
		}
	}
	file, _, err := f.h.updateEnv.fs.read(workspaceobserve.UnitPath)
	if err != nil || !file.Present || executor.disables != 0 || f.h.r.Update == nil || f.state.Open {
		t.Fatal("unknown stop changed removal or admission", err)
	}
}
