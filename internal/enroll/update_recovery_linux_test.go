//go:build linux

package enroll

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/ShaulLavo/brine/internal/admission"
)

func TestUpdateRecoveryAtEverySave(t *testing.T) {
	for crash := 1; crash <= 28; crash++ {
		t.Run(fmt.Sprint(crash), func(t *testing.T) { updateRecoveryControl(t, crash) })
	}
}
func updateRecoveryControl(t *testing.T, crash int) {
	t.Helper()
	f := newUpdateFixture(t)
	plan := fixtureUpdatePlan(t, f)
	f.failSave = f.saves + crash
	_, err := f.h.applyUpdate(context.Background(), plan)
	if err == nil {
		return
	}
	if !strings.Contains(err.Error(), "fixture crash before save") {
		t.Fatal("unexpected failure", err)
	}
	f.restart(t)
	if f.h.r.Update == nil {
		if err := f.h.requireFreshAdmission(context.Background(), false); err != nil {
			t.Fatal("admitting orphan did not reopen", err)
		}
		return
	}
	request := HostRequest{Action: "update-apply", HostKey: plan.HostKey, Confirmed: plan.Digest}
	if _, err := f.h.continueUpdate(context.Background(), request); err != nil {
		t.Fatal("continuation failed", err)
	}
	if !f.state.Open || f.h.r.Update != nil {
		t.Fatal("terminal reopening did not finish")
	}
	for unit, count := range f.launches {
		if count != 1 {
			t.Fatalf("native effect repeated %s %d", unit, count)
		}
	}
}
func TestUpdateAdmissionInodesAndInstallOrder(t *testing.T) {
	f := newUpdateFixture(t)
	names := []string{}
	for _, step := range f.h.steps() {
		names = append(names, step.Name)
	}
	for _, name := range []string{"admission", "admission-settle"} {
		if indexTrace(names, name) < 0 || indexTrace(names, name) > indexTrace(names, "user") {
			t.Fatal("user could run before admission artifacts", names)
		}
	}
	if !strings.Contains(settleUnit, "Before=systemd-user-sessions.service") || !strings.Contains(settleUnit, "WantedBy=multi-user.target") {
		t.Fatal("boot ordering missing")
	}
	if err := f.h.installAdmission(context.Background()); err != nil {
		t.Fatal(err)
	}
	identity := *f.h.r.Admission
	if err := f.h.installAdmission(context.Background()); err != nil || *f.h.r.Admission != identity {
		t.Fatal("second installation replaced locks", err)
	}
	path := f.h.updateEnv.fs.path(admissionDir + "/lock")
	original, err := proofOpen(path, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := original.Close(); err != nil {
			t.Error(err)
		}
	}()
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	f.write(t, admissionDir+"/lock", nil, 0644)
	if err := f.h.bindAdmission(); err == nil {
		t.Fatal("replacement lock inode accepted")
	}
}
func TestUpdateBootLeavesNonterminalClosed(t *testing.T) {
	f := newUpdateFixture(t)
	plan := fixtureUpdatePlan(t, f)
	f.pending(t, plan)
	f.h.r.Update.Stage = admission.Migrating
	if err := f.h.resumeUpdateTerminal(context.Background()); err == nil || f.state.Open {
		t.Fatal("nonterminal update boot-reopened")
	}
}
func TestUpdateStageRevokePrecedesDestructiveProof(t *testing.T) {
	f := newUpdateFixture(t)
	plan := fixtureUpdatePlan(t, f)
	f.pending(t, plan)
	if err := f.h.stageUpdateArtifacts(); err != nil {
		t.Fatal(err)
	}
	d := fixtureUpdateDrain{f}
	if err := d.RevokeAndDrain(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := f.h.replaceUpdateArtifacts(context.Background(), d, false); err != nil {
		t.Fatal(err)
	}
	if indexTrace(f.trace, "revoke") > indexTrace(f.trace, "proof") || indexTrace(f.trace, "L4") > indexTrace(f.trace, "proof") {
		t.Fatal("proof preceded stage drain", f.trace)
	}
}

func TestUpdateOrphanAbandonmentCrashTails(t *testing.T) {
	for crash := 1; crash <= 5; crash++ {
		t.Run(fmt.Sprint(crash), func(t *testing.T) { orphanUpdateCrash(t, crash) })
	}
}
func orphanUpdateCrash(t *testing.T, crash int) {
	t.Helper()
	f := newUpdateFixture(t)
	plan := fixtureUpdatePlan(t, f)
	f.pending(t, plan)
	f.state.Pending.Stage = admission.Admitting
	f.h.r.Update = nil
	if err := f.h.updateSave(); err != nil {
		t.Fatal(err)
	}
	f.failSave = f.saves + crash
	err := f.h.requireFreshAdmission(context.Background(), false)
	if err == nil {
		return
	}
	if !strings.Contains(err.Error(), "fixture crash before save") {
		t.Fatal(err)
	}
	f.restart(t)
	if err := f.h.requireFreshAdmission(context.Background(), false); err != nil {
		t.Fatal("orphan tail did not converge", err)
	}
	if !f.state.Open || f.h.r.AdmissionAbandonment != nil {
		t.Fatal("abandonment tail was lost")
	}
}
