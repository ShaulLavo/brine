//go:build linux

package enroll

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ShaulLavo/brine/internal/admission"
	"github.com/ShaulLavo/brine/internal/localexec"
	"golang.org/x/sys/unix"
)

type bootstrapFixtureExecutor struct {
	f      *updateFixture
	expiry string
	active bool
}

func (e *bootstrapFixtureExecutor) Execute(ctx context.Context, c localexec.Command) (localexec.Result, error) {
	if c.Path == "/usr/sbin/sshd" {
		return localexec.Result{Stdout: "usepam yes\n"}, nil
	}
	if c.Path == "getent" && c.Args[0] == "shadow" {
		return localexec.Result{Stdout: strings.Join([]string{"brine", "!", "1", "0", "99999", "7", "", e.expiry, ""}, ":") + "\n"}, nil
	}
	if c.Path == "/usr/sbin/usermod" {
		e.expiry = c.Args[1]
		if e.expiry == "-1" {
			e.expiry = ""
		}
		e.f.trace = append(e.f.trace, "expiry:"+e.expiry)
		return localexec.Result{}, nil
	}
	if c.Path == "loginctl" {
		return e.linger(c.Args[0])
	}
	if c.Path == "systemctl" && len(c.Args) > 1 && c.Args[0] == "show" && c.Args[1] == e.f.h.fenceUnit() {
		state := "inactive"
		if e.active {
			state = "active"
		}
		return localexec.Result{Stdout: state + "\n"}, nil
	}
	if c.Path == "systemctl" && len(c.Args) > 1 && c.Args[0] == "stop" && c.Args[1] == e.f.h.fenceUnit() {
		e.active = false
		e.f.trace = append(e.f.trace, "stop-manager")
		return localexec.Result{}, nil
	}
	return (updateFixtureExecutor{e.f}).Execute(ctx, c)
}
func (e *bootstrapFixtureExecutor) linger(verb string) (localexec.Result, error) {
	path := e.f.h.updateEnv.fs.path("/var/lib/systemd/linger/brine")
	switch verb {
	case "disable-linger":
		err := os.Remove(path)
		if errors.Is(err, os.ErrNotExist) {
			err = nil
		}
		e.f.trace = append(e.f.trace, "linger-off")
		return localexec.Result{}, err
	case "enable-linger":
		return localexec.Result{}, os.WriteFile(path, nil, 0600)
	case "terminate-user":
		return localexec.Result{}, nil
	default:
		return localexec.Result{}, errors.New("unexpected loginctl verb")
	}
}
func bootstrapUpdateFixture(t *testing.T) (*updateFixture, UpdatePlan) {
	t.Helper()
	f := newUpdateFixture(t)
	plan := fixtureUpdatePlan(t, f)
	fs := f.h.updateEnv.fs
	for _, name := range []string{"state.json", "lock", "stage.lock"} {
		if err := fs.remove(admissionDir + "/" + name); err != nil {
			t.Fatal(err)
		}
		delete(f.h.r.Files, admissionDir+"/"+name)
	}
	if err := os.Remove(fs.path(admissionDir)); err != nil {
		t.Fatal(err)
	}
	delete(f.h.r.Dirs, admissionDir)
	f.h.r.Admission = nil
	if err := fs.remove(settleUnitPath); err != nil {
		t.Fatal(err)
	}
	delete(f.h.r.Files, settleUnitPath)
	plan.Artifacts = nil
	if err := f.h.planUpdateArtifacts(&plan, ""); err != nil {
		t.Fatal(err)
	}
	plan.ObserverRestart = false
	if err := os.MkdirAll(fs.path("/etc/pam.d"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(fs.path("/etc/pam.d/runuser"), []byte("auth sufficient pam_rootok.so\nsession required pam_unix.so\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(fs.path("/var/lib/systemd/linger/brine"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	f.h.exec = &bootstrapFixtureExecutor{f: f, active: true}
	plan.Bootstrap = true
	plan.Quiescence = admission.Runner
	plan.Digest = updateDigest(plan)
	oldDrain := f.h.updateEnv.drain
	f.h.updateEnv.drain = func(ctx context.Context, p admission.Pending, fresh bool) (updateDrain, error) {
		if fresh || !f.state.Open {
			return oldDrain(ctx, p, fresh)
		}
		_, raw, err := fs.read(admissionDir + "/state.json")
		if err != nil {
			return nil, err
		}
		f.state, err = admission.Decode(raw)
		if err != nil {
			return nil, err
		}
		return oldDrain(ctx, p, fresh)
	}
	return f, plan
}
func TestUpdateBootstrapFencesBeforeAdmissionInstall(t *testing.T) {
	f, plan := bootstrapUpdateFixture(t)
	if _, err := f.h.applyUpdate(context.Background(), plan); err != nil {
		t.Fatal(err)
	}
	if indexTrace(f.trace, "expiry:1") < 0 || indexTrace(f.trace, "linger-off") < indexTrace(f.trace, "expiry:1") || indexTrace(f.trace, "stop-manager") < indexTrace(f.trace, "linger-off") || indexTrace(f.trace, "L2") < indexTrace(f.trace, "stop-manager") {
		t.Fatal("bootstrap lock/fence order", f.trace)
	}
	if f.h.r.Admission == nil || !f.state.Open {
		t.Fatal("bootstrap did not install and reopen admission")
	}
}
func TestUpdateBootstrapAbortDoesNotCallPregateCommand(t *testing.T) {
	f, plan := bootstrapUpdateFixture(t)
	u, err := newUpdateRecord(plan)
	if err != nil {
		t.Fatal(err)
	}
	u.PriorIntents = maps.Clone(f.h.r.Journal.Intents)
	f.h.r.Update = u
	if err := f.h.stageUpdateArtifacts(); err != nil {
		t.Fatal(err)
	}
	if err := f.h.fenceRunner(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := f.h.installAdmission(context.Background()); err != nil {
		t.Fatal(err)
	}
	d, err := f.h.updateEnv.drain(context.Background(), f.h.updatePending(), false)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := d.Close(); err != nil {
			t.Error(err)
		}
	}()
	if err := f.h.captureUpdate(context.Background(), d); err != nil {
		t.Fatal(err)
	}
	if err := f.h.replaceUpdateArtifact(0, plan.Artifacts[0], false); err != nil {
		t.Fatal(err)
	}
	u.Aborting = true
	result, err := f.h.abortUpdate(context.Background(), d)
	if err != nil || !result.(UpdateResult).Aborted {
		t.Fatal(err)
	}
	if _, err := os.Stat(f.h.updateEnv.fs.path(admissionDir)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("bootstrap admission removal did not finish", err)
	}
	_, binary, err := f.h.updateEnv.fs.read(binaryPath)
	if err != nil || string(binary) != "previous" {
		t.Fatal("original binary not restored", err)
	}
	_, updater, err := f.h.updateEnv.fs.read(filepath.Join(updateStagePath(u.ID), "updater"))
	if err != nil || string(updater) != "candidate" {
		t.Fatal("retained verifier was lost", err)
	}
}

func TestUpdateAbortDisablesAddedSettleBeforeRemovingItsFile(t *testing.T) {
	f, plan := bootstrapUpdateFixture(t)
	u, err := newUpdateRecord(plan)
	if err != nil {
		t.Fatal(err)
	}
	u.PriorIntents = maps.Clone(f.h.r.Journal.Intents)
	f.h.r.Update = u
	if err := f.h.stageUpdateArtifacts(); err != nil {
		t.Fatal(err)
	}
	if err := f.h.fenceRunner(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := f.h.installAdmission(context.Background()); err != nil {
		t.Fatal(err)
	}
	d, err := f.h.updateEnv.drain(context.Background(), f.h.updatePending(), false)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := d.Close(); err != nil {
			t.Error(err)
		}
	}()
	if err := f.h.captureUpdate(context.Background(), d); err != nil {
		t.Fatal(err)
	}
	if err := f.h.replaceUpdateArtifacts(context.Background(), d, false); err != nil {
		t.Fatal(err)
	}
	if err := f.h.issueUpdateEffect(context.Background(), &u.Activation.Enable, "settle-enable", "/usr/bin/systemctl", "enable", "brine-admission-settle.service"); err != nil {
		t.Fatal(err)
	}
	u.Aborting = true
	if _, err := f.h.abortUpdate(context.Background(), d); err != nil {
		t.Fatal(err)
	}
	if f.settleDisables != 1 || !f.disableSawSettle || !u.RollbackActivation.Enable.Done {
		t.Fatal("added settle enablement was not withdrawn before file removal")
	}
	file, _, err := f.h.updateEnv.fs.read(settleUnitPath)
	if err != nil || file.Present {
		t.Fatal("added settle unit survived abort", err)
	}
}

func TestUpdateBootstrapAdmissionRemovalCrashTails(t *testing.T) {
	for crash := 1; crash <= 4; crash++ {
		t.Run(fmt.Sprint(crash), func(t *testing.T) { bootstrapRemovalCrash(t, crash) })
	}
}
func bootstrapRemovalCrash(t *testing.T, crash int) {
	t.Helper()
	f, u := bootstrapRemovalFixture(t)
	f.failSave = f.saves + crash
	if err := f.h.completeBootstrapRemoval(context.Background()); err == nil {
		t.Fatal("removal-tail crash did not fire")
	}
	f.restart(t)
	if err := f.h.completeBootstrapRemoval(context.Background()); err != nil {
		t.Fatal("bootstrap removal tail did not converge", err)
	}
	if f.h.r.Update != nil || f.h.r.Admission != nil {
		t.Fatal("terminal bootstrap record remained")
	}
	file, _, err := f.h.updateEnv.fs.read(updateStagePath(u.ID) + "/updater")
	if err != nil || file.SHA256 != u.Plan.CandidateSHA256 {
		t.Fatal("abort discarded its retained verifier", err)
	}
}

func bootstrapRemovalFixture(t *testing.T) (*updateFixture, *updateRecord) {
	t.Helper()
	f, plan := bootstrapUpdateFixture(t)
	u, err := newUpdateRecord(plan)
	if err != nil {
		t.Fatal(err)
	}
	u.Aborting = true
	u.Stage = admission.Terminal
	u.RemovalPending = true
	u.RemovedAdmission = [3]bool{}
	u.Reopening = &updateReopening{Reconcile: updateEffect{Done: true, Observed: "native-ack"}}
	f.h.r.Update = u
	if err := f.h.stageUpdateArtifacts(); err != nil {
		t.Fatal(err)
	}
	if err := f.h.installAdmission(context.Background()); err != nil {
		t.Fatal(err)
	}
	p := f.h.updatePending()
	p.Stage = admission.Terminal
	raw, err := admission.Encode(admission.State{Version: 1, Pending: &p})
	if err != nil {
		t.Fatal(err)
	}
	f.write(t, admissionDir+"/state.json", raw, 0644)
	if err := f.h.updateSave(); err != nil {
		t.Fatal(err)
	}
	return f, u
}

func TestUpdateBootstrapRemovalResumeDrainsPausedLeases(t *testing.T) {
	for _, name := range []string{"lock", "stage.lock"} {
		t.Run(name, func(t *testing.T) { bootstrapPausedLease(t, name) })
	}
}
func bootstrapPausedLease(t *testing.T, name string) {
	t.Helper()
	f, _ := bootstrapRemovalFixture(t)
	file, err := proofOpen(f.h.updateEnv.fs.path(admissionDir+"/"+name), unix.O_RDONLY)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := file.Close(); err != nil {
			t.Error(err)
		}
	}()
	if err := unix.Flock(int(file.Fd()), unix.LOCK_SH|unix.LOCK_NB); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err := f.h.completeBootstrapRemoval(ctx); !errors.Is(err, admission.ErrDrainTimeout) {
		t.Fatal("removal did not wait for paused lease", err)
	}
	if f.h.r.Update == nil || f.h.r.Update.RemovedAdmission != ([3]bool{}) {
		t.Fatal("removal changed artifacts before lease drain")
	}
	if err := unix.Flock(int(file.Fd()), unix.LOCK_UN); err != nil {
		t.Fatal(err)
	}
	if err := f.h.completeBootstrapRemoval(context.Background()); err != nil {
		t.Fatal("drained removal failed", err)
	}
}

func TestUpdateBootstrapRemovalRejectsOpenAdmission(t *testing.T) {
	f, _ := bootstrapRemovalFixture(t)
	raw, err := admission.Encode(admission.State{Version: 1, Open: true})
	if err != nil {
		t.Fatal(err)
	}
	f.write(t, admissionDir+"/state.json", raw, 0644)
	if err := f.h.completeBootstrapRemoval(context.Background()); !errors.Is(err, admission.ErrInconsistent) {
		t.Fatal("open gate accepted during removal", err)
	}
	if f.h.r.Update.RemovedAdmission != ([3]bool{}) {
		t.Fatal("inconsistent tail removed artifacts")
	}
}
