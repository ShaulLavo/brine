//go:build linux

package enroll

import (
	"context"
	"errors"
	"maps"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ShaulLavo/brine/internal/admission"
	"github.com/ShaulLavo/brine/internal/caddy"
	"github.com/ShaulLavo/brine/internal/localexec"
)

func fixtureUpdatePlan(t *testing.T, f *updateFixture) UpdatePlan {
	t.Helper()
	h := f.h
	for id, a := range enrollmentArtifacts() {
		if a.class == artifactOperatorOwned || a.class == artifactAdmission {
			continue
		}
		data := []byte("pinned artifact")
		if a.render != nil {
			data = a.render(h)
		}
		if enrollmentArtifactID(id) == artifactCaddyImport {
			data = fixtureCaddyAuthority(t)
		}
		if enrollmentArtifactID(id) == artifactBinary {
			data = []byte("previous")
		}
		observed := f.write(t, a.path, data, uint32(a.mode))
		h.r.Files[a.path] = ownedFile{Before: []byte("pre-enrollment-original"), Existed: true, OriginalMode: 0600, OriginalUID: 71, OriginalGID: 72, Hash: observed.SHA256, Mode: observed.Mode}
	}
	if err := h.installAdmission(context.Background()); err != nil {
		t.Fatal(err)
	}
	plan := UpdatePlan{EnrollmentID: h.r.ID, HostKey: "fixture-host-key", CandidateSHA256: hash(h.source), CandidateVersion: "candidate", RecordedBinarySHA256: h.r.BinaryHash, Quiescence: admission.Store, ControlAbsent: true}
	if err := h.planUpdateArtifacts(&plan, ""); err != nil {
		t.Fatal(err)
	}
	// Observer activation has its separate native readback controls below.
	plan.ObserverRestart = false
	plan.Digest = updateDigest(plan)
	return plan
}
func TestUpdateApplyManifestPreservesOriginals(t *testing.T) {
	f := newUpdateFixture(t)
	plan := fixtureUpdatePlan(t, f)
	original := f.h.r.Files[binaryPath]
	beforeIdentity := *f.h.r.Admission
	result, err := f.h.applyUpdate(context.Background(), plan)
	if err != nil {
		t.Fatal(err)
	}
	if !result.(UpdateResult).Committed || f.h.r.Update != nil || !f.state.Open {
		t.Fatal("update did not finish terminal tail")
	}
	actual := f.h.r.Files[binaryPath]
	if string(actual.Before) != string(original.Before) || actual.OriginalMode != original.OriginalMode || actual.OriginalUID != original.OriginalUID || actual.OriginalGID != original.OriginalGID || actual.Hash != hash(f.h.source) {
		t.Fatal("pre-enrollment bytes or metadata changed")
	}
	if err := f.h.bindAdmission(); err != nil || *f.h.r.Admission != beforeIdentity {
		t.Fatal("admission lock inode replaced", err)
	}
	for unit, count := range f.launches {
		if count != 1 {
			t.Fatalf("issuer %s delivered %d times", unit, count)
		}
	}
	open := indexTrace(f.trace, "release-L2")
	reconcile := indexTracePrefix(f.trace, "issue:brine-update-", "reconcile.service")
	if open < 0 || reconcile <= open {
		t.Fatal("runner handoff occurred under L2", f.trace)
	}
}
func indexTrace(trace []string, want string) int {
	for index, value := range trace {
		if value == want {
			return index
		}
	}
	return -1
}
func indexTracePrefix(trace []string, prefix, suffix string) int {
	for index, value := range trace {
		if strings.HasPrefix(value, prefix) && strings.HasSuffix(value, suffix) {
			return index
		}
	}
	return -1
}
func TestUpdateArtifactRenameBeforeSave(t *testing.T) {
	f := newUpdateFixture(t)
	plan := fixtureUpdatePlan(t, f)
	f.pending(t, plan)
	if err := f.h.stageUpdateArtifacts(); err != nil {
		t.Fatal(err)
	}
	item := plan.Artifacts[0]
	f.failSave = f.saves + 1
	if err := f.h.replaceUpdateArtifact(0, item, false); err == nil {
		t.Fatal("crash control did not fire")
	}
	f.restart(t)
	if err := f.h.replaceUpdateArtifact(0, item, false); err != nil {
		t.Fatal("rename before save did not converge", err)
	}
	if !f.h.r.Update.Artifacts[0].Done {
		t.Fatal("replacement was not recorded")
	}
	if err := f.h.restoreUpdateArtifact(0, item, *f.h.r.Update.Artifacts[0].Installed); err != nil {
		t.Fatal(err)
	}
	if err := f.h.replaceUpdateArtifact(0, item, true); err != nil {
		t.Fatal("second restoration did not converge", err)
	}
}
func TestUpdateDifferentCandidateAndThirdPartyRefused(t *testing.T) {
	f := newUpdateFixture(t)
	plan := fixtureUpdatePlan(t, f)
	f.pending(t, plan)
	if err := f.h.stageUpdateArtifacts(); err != nil {
		t.Fatal(err)
	}
	f.h.source = []byte("different")
	if _, err := f.h.continueUpdate(context.Background(), HostRequest{Action: "update-apply"}); err == nil || err.Error() != "update_pending_different_candidate" {
		t.Fatal("different candidate accepted", err)
	}
	f.h.source = []byte("candidate")
	f.write(t, binaryPath, []byte("third party"), 0755)
	if err := f.h.replaceUpdateArtifact(0, plan.Artifacts[0], false); err == nil {
		t.Fatal("third-party binary adopted")
	}
}
func TestUpdateBoundaryClassesAndDriftOverride(t *testing.T) {
	for _, kind := range []string{"authority", "tool", "wrong-override", "exact-override"} {
		t.Run(kind, func(t *testing.T) { updateBoundaryControl(t, kind) })
	}
}
func updateBoundaryControl(t *testing.T, kind string) {
	t.Helper()
	f := newUpdateFixture(t)
	_ = fixtureUpdatePlan(t, f)
	override := ""
	switch kind {
	case "authority":
		f.h.identityKey = []byte("different authority")
	case "tool":
		delete(f.h.r.Files, NativeReaderPinPath)
	default:
		observed := f.write(t, binaryPath, []byte("unrecorded"), 0755)
		override = observed.SHA256
	}
	if kind == "wrong-override" {
		override = hash([]byte("wrong"))
	}
	plan := UpdatePlan{}
	err := f.h.planUpdateArtifacts(&plan, override)
	if (err == nil) != (kind == "exact-override") {
		t.Fatal("class boundary control", kind, err)
	}
}
func TestUpdateActivationLostReplyDoesNotRestart(t *testing.T) {
	f := newUpdateFixture(t)
	plan := fixtureUpdatePlan(t, f)
	f.pending(t, plan)
	f.effectError = true
	e := &f.h.r.Update.Activation.Observer
	if err := f.h.issueUpdateEffect(context.Background(), e, "observer", "/usr/bin/systemctl", "restart", "brine-workspace-observer.service"); err != nil {
		t.Fatal("native success was not recovered", err)
	}
	e.Done = false
	if err := f.h.issueUpdateEffect(context.Background(), e, "observer", "/usr/bin/systemctl", "restart", "brine-workspace-observer.service"); err != nil {
		t.Fatal(err)
	}
	if f.launches[f.h.effectName("observer")] != 1 {
		t.Fatal("lost reply reissued restart")
	}
}
func TestUpdateActivationIntentWithoutDeliveryStaysPending(t *testing.T) {
	f := newUpdateFixture(t)
	plan := fixtureUpdatePlan(t, f)
	f.pending(t, plan)
	e := &f.h.r.Update.Activation.Observer
	e.Intent = true
	if err := f.h.issueUpdateEffect(context.Background(), e, "observer", "/usr/bin/systemctl", "restart", "brine-workspace-observer.service"); err == nil {
		t.Fatal("unknown delivery treated as successful")
	}
	if len(f.launches) != 0 {
		t.Fatal("unknown delivery was retried")
	}
}
func TestUpdatePendingAndInconsistentJoins(t *testing.T) {
	f := newUpdateFixture(t)
	plan := fixtureUpdatePlan(t, f)
	f.pending(t, plan)
	if err := f.h.requireFreshAdmission(context.Background(), false); err == nil || err.Error() != "pending_update" {
		t.Fatal("pending update admitted", err)
	}
	closed := f.state
	f.state = admission.State{Version: 1, Open: true}
	if err := f.h.requireFreshAdmission(context.Background(), false); !errors.Is(err, admission.ErrInconsistent) {
		t.Fatal("open/nonterminal root record mismatch accepted", err)
	}
	f.state = closed
	f.h.r.Update = nil
	f.state.Pending.Stage = admission.Replacing
	if err := f.h.requireFreshAdmission(context.Background(), false); !errors.Is(err, admission.ErrInconsistent) {
		t.Fatal("orphan pending admitted", err)
	}
}
func TestUpdateRawCaptureDoesNotCreateAbsentStore(t *testing.T) {
	f := newUpdateFixture(t)
	capture, facts, err := f.h.inspectUpdateStore(context.Background())
	if err != nil || !capture.Absent || !facts.Absent {
		t.Fatal(err)
	}
	if _, err := os.Stat(f.h.updateEnv.fs.path("/" + admissionState + "/control.db")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("planning created a store", err)
	}
}

func fixtureCaddyAuthority(t *testing.T) []byte {
	t.Helper()
	data, err := caddy.EnrollmentRoot([]byte("pre-enrollment-original"))
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestUpdateCleansOnlyOwnedAtomicTemporaries(t *testing.T) {
	f := newUpdateFixture(t)
	plan := fixtureUpdatePlan(t, f)
	f.pending(t, plan)
	if err := f.h.stageUpdateArtifacts(); err != nil {
		t.Fatal(err)
	}
	f.write(t, "/usr/local/bin/.brine-write-fixture", []byte("interrupted"), 0600)
	f.write(t, "/usr/local/bin/unrelated", []byte("keep"), 0600)
	if err := f.h.cleanupUpdateTemporaries(); err != nil {
		t.Fatal(err)
	}
	observed, _, err := f.h.updateEnv.fs.read("/usr/local/bin/.brine-write-fixture")
	if err != nil || observed.Present {
		t.Fatal("owned orphan was not cleaned", err)
	}
	observed, _, err = f.h.updateEnv.fs.read("/usr/local/bin/unrelated")
	if err != nil || !observed.Present {
		t.Fatal("unrelated file removed", err)
	}
}

func TestUpdateUnrecordedAbortRefusalPreservesForwardResume(t *testing.T) {
	f := newUpdateFixture(t)
	plan := fixtureUpdatePlan(t, f)
	observed := f.write(t, binaryPath, []byte("unrecorded"), 0755)
	plan.Artifacts = nil
	if err := f.h.planUpdateArtifacts(&plan, observed.SHA256); err != nil {
		t.Fatal(err)
	}
	plan.ObserverRestart = false
	plan.Digest = updateDigest(plan)
	f.pending(t, plan)
	if err := f.h.stageUpdateArtifacts(); err != nil {
		t.Fatal(err)
	}
	request := HostRequest{Action: "update-abort", HostKey: plan.HostKey, Confirmed: plan.Digest}
	if _, err := f.h.continueUpdate(context.Background(), request); err == nil {
		t.Fatal("unrecorded rollback accepted")
	}
	if f.h.r.Update.Aborting {
		t.Fatal("refused rollback disabled forward resume")
	}
	request.Action = "update-apply"
	if _, err := f.h.continueUpdate(context.Background(), request); err != nil {
		t.Fatal("forward continuation stranded", err)
	}
}
func TestUpdateRetainsOnlyLatestStaging(t *testing.T) {
	f := newUpdateFixture(t)
	plan := fixtureUpdatePlan(t, f)
	first, err := f.h.applyUpdate(context.Background(), plan)
	if err != nil {
		t.Fatal(err)
	}
	old := first.(UpdateResult).Retained
	f.h.source = []byte("candidate2")
	plan.CandidateSHA256 = hash(f.h.source)
	plan.RecordedBinarySHA256 = f.h.r.BinaryHash
	plan.Artifacts = nil
	if err := f.h.planUpdateArtifacts(&plan, ""); err != nil {
		t.Fatal(err)
	}
	plan.ObserverRestart = false
	plan.Digest = updateDigest(plan)
	second, err := f.h.applyUpdate(context.Background(), plan)
	if err != nil {
		t.Fatal(err)
	}
	if old == second.(UpdateResult).Retained {
		t.Fatal("staging identity reused")
	}
	if _, err := os.Stat(f.h.updateEnv.fs.path(old)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("older staging retained", err)
	}
}
func TestUpdateFDReferencesRequireActualStoreIdentity(t *testing.T) {
	store := map[string]admissionFileIdentity{"control.db": {dev: 1, ino: 2}}
	if updateFDReferences(map[string]admissionFDView{"4": {identity: admissionFileIdentity{dev: 2, ino: 2}}}, store) {
		t.Fatal("inode without device treated as holder")
	}
	if !updateFDReferences(map[string]admissionFDView{"4": {identity: admissionFileIdentity{dev: 1, ino: 2}}}, store) {
		t.Fatal("real holder missed")
	}
}

func TestUpdateAbortWaitsForUnknownActivationDelivery(t *testing.T) {
	f := newUpdateFixture(t)
	plan := fixtureUpdatePlan(t, f)
	f.pending(t, plan)
	if err := f.h.stageUpdateArtifacts(); err != nil {
		t.Fatal(err)
	}
	f.h.r.Update.Activation.Observer.Intent = true
	request := HostRequest{Action: "update-abort", HostKey: plan.HostKey, Confirmed: plan.Digest}
	if _, err := f.h.continueUpdate(context.Background(), request); err == nil {
		t.Fatal("abort raced an unknown activation issuer")
	}
	if f.h.r.Update.Aborting {
		t.Fatal("refused abort changed the transaction direction")
	}
	observed, _, err := f.h.updateEnv.fs.read(binaryPath)
	if err != nil || observed.SHA256 != plan.Artifacts[0].Before.SHA256 {
		t.Fatal("unknown activation changed rollback artifacts", err)
	}
}

func TestUpdateDoesNotRelaxApplyDifferentBinaryRefusal(t *testing.T) {
	f := newUpdateFixture(t)
	if err := f.h.checkApplyIntent(f.h.r.Key); err == nil || err.Error() != "enrollment options differ from durable intent" {
		t.Fatal("ordinary apply accepted a new binary", err)
	}
	f.h.source = []byte("previous")
	if err := f.h.checkApplyIntent(f.h.r.Key); err != nil {
		t.Fatal("original apply continuation refused", err)
	}
}

type retiredIssuerFixture struct{ calls int }

func (f *retiredIssuerFixture) Execute(_ context.Context, c localexec.Command) (localexec.Result, error) {
	f.calls++
	if c.Mutation {
		return localexec.Result{}, errors.New("retired issuer was stopped again")
	}
	return localexec.Result{Stdout: "not-found\n", ExitCode: 1}, &localexec.Error{Kind: localexec.Failed, ExitCode: 1}
}
func TestUpdateRetirementAcceptsCompletedIssuerAlreadyGone(t *testing.T) {
	f := newUpdateFixture(t)
	runner := &retiredIssuerFixture{}
	f.h.exec = runner
	if err := f.h.retireUpdateIssuer(context.Background(), "brine-update-fixture-forward-observer.service"); err != nil {
		t.Fatal("completed issuer absence stranded reopening", err)
	}
	if runner.calls != 1 {
		t.Fatal("retired issuer triggered another mutation")
	}
}

func TestUpdateAbortRetainsCommittedPointWithoutOrphaningItsStaging(t *testing.T) {
	f := newUpdateFixture(t)
	plan := fixtureUpdatePlan(t, f)
	first, err := f.h.applyUpdate(context.Background(), plan)
	if err != nil {
		t.Fatal(err)
	}
	committedID := first.(UpdateResult).ID
	f.h.source = []byte("candidate2")
	plan.CandidateSHA256 = hash(f.h.source)
	plan.RecordedBinarySHA256 = f.h.r.BinaryHash
	plan.Artifacts = nil
	if err := f.h.planUpdateArtifacts(&plan, ""); err != nil {
		t.Fatal(err)
	}
	plan.ObserverRestart = false
	plan.Digest = updateDigest(plan)
	f.pending(t, plan)
	u := f.h.r.Update
	u.PreviousUpdate = committedID
	u.PriorIntents = maps.Clone(f.h.r.Journal.Intents)
	if err := f.h.stageUpdateArtifacts(); err != nil {
		t.Fatal(err)
	}
	d := fixtureUpdateDrain{f}
	if err := f.h.captureUpdate(context.Background(), d); err != nil {
		t.Fatal(err)
	}
	if err := f.h.replaceUpdateArtifacts(context.Background(), d, false); err != nil {
		t.Fatal(err)
	}
	u.Aborting = true
	if _, err := f.h.abortUpdate(context.Background(), d); err != nil {
		t.Fatal(err)
	}
	if f.h.r.LastUpdate != committedID || f.h.r.AbortedUpdate != u.ID {
		t.Fatal("abort replaced the committed recovery point")
	}
	f.h.source = []byte("candidate3")
	plan.CandidateSHA256 = hash(f.h.source)
	plan.RecordedBinarySHA256 = f.h.r.BinaryHash
	plan.Artifacts = nil
	if err := f.h.planUpdateArtifacts(&plan, ""); err != nil {
		t.Fatal(err)
	}
	plan.ObserverRestart = false
	plan.Digest = updateDigest(plan)
	if _, err := f.h.applyUpdate(context.Background(), plan); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{committedID, u.ID} {
		if _, err := os.Stat(f.h.updateEnv.fs.path(updateStagePath(id))); !errors.Is(err, os.ErrNotExist) {
			t.Fatal("obsolete recovery staging was orphaned", id, err)
		}
	}
	if f.h.r.AbortedUpdate != "" {
		t.Fatal("committed update retained an obsolete abort pointer")
	}
}

func TestUpdateMissingAddableParentRefusesBeforeEffects(t *testing.T) {
	f := newUpdateFixture(t)
	_ = fixtureUpdatePlan(t, f)
	path := reconcileGeneratorPath
	if err := f.h.updateEnv.fs.remove(path); err != nil {
		t.Fatal(err)
	}
	delete(f.h.r.Files, path)
	if err := os.Remove(f.h.updateEnv.fs.path(filepath.Dir(path))); err != nil {
		t.Fatal(err)
	}
	plan := UpdatePlan{}
	if err := f.h.planUpdateArtifacts(&plan, ""); err == nil || !strings.Contains(err.Error(), "artifact_parent_unavailable") {
		t.Fatal("missing unowned parent admitted", err)
	}
	if f.h.r.Update != nil || len(f.launches) != 0 {
		t.Fatal("parent refusal performed effects")
	}
}
func TestUpdateMissingUninstalledParentDoesNotStrandAbort(t *testing.T) {
	f := newUpdateFixture(t)
	plan := fixtureUpdatePlan(t, f)
	if err := f.h.updateEnv.fs.remove(reconcileGeneratorPath); err != nil {
		t.Fatal(err)
	}
	delete(f.h.r.Files, reconcileGeneratorPath)
	plan.Artifacts = nil
	if err := f.h.planUpdateArtifacts(&plan, ""); err != nil {
		t.Fatal(err)
	}
	plan.ObserverRestart = false
	plan.Digest = updateDigest(plan)
	f.pending(t, plan)
	f.h.r.Update.Aborting = true
	if err := f.h.stageUpdateArtifacts(); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(f.h.updateEnv.fs.path(filepath.Dir(reconcileGeneratorPath))); err != nil {
		t.Fatal(err)
	}
	if _, err := f.h.abortUpdate(context.Background(), fixtureUpdateDrain{f}); err != nil {
		t.Fatal("an absent uninstalled parent stranded abort", err)
	}
}

func TestUpdateRefusesWritableArtifactAncestor(t *testing.T) {
	f := newUpdateFixture(t)
	_ = fixtureUpdatePlan(t, f)
	if err := os.Chmod(f.h.updateEnv.fs.path("/usr"), 0777); err != nil {
		t.Fatal(err)
	} // #nosec G302 -- This private fixture deliberately models a writable authority ancestor.
	if err := f.h.planUpdateArtifacts(&UpdatePlan{}, ""); err == nil {
		t.Fatal("writable ancestor admitted for artifact replacement")
	}
	if _, err := f.h.updateEnv.fs.write(binaryPath, []byte("foreign"), 0755, os.Geteuid(), os.Getegid()); err == nil {
		t.Fatal("write crossed a writable authority ancestor")
	}
	data, err := proofRead(context.Background(), f.h.updateEnv.fs.path(binaryPath), 1024)
	if err != nil || data != "previous" {
		t.Fatal("refusal changed the installed bytes", err)
	}
}
