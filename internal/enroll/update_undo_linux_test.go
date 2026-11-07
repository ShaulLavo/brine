//go:build linux

package enroll

import (
	"errors"
	"maps"
	"os"
	"path/filepath"
	"testing"
)

// Only the final record-retirement tail runs here. Earlier host removal is not
// claimed by this private filesystem fixture.
func updateUndoTailFixture(t *testing.T) *updateFixture {
	t.Helper()
	f := newUpdateFixture(t)
	plan := fixtureUpdatePlan(t, f)
	if _, err := f.h.applyUpdate(t.Context(), plan); err != nil {
		t.Fatal(err)
	}
	f.h.r.Journal.Phase = Removed
	if err := f.h.updateSave(); err != nil {
		t.Fatal(err)
	}
	f.write(t, recordDir+"/record.json", f.saved, 0600)
	f.write(t, recordDir+"/lock", nil, 0600)
	return f
}

func TestUpdateUndoRetiresRecordedStaging(t *testing.T) {
	f := updateUndoTailFixture(t)
	if err := f.h.removeEnrollmentRecord(t.Context()); err != nil {
		t.Fatal("undo lost its retained update journal", err)
	}
	if _, err := os.Lstat(f.h.updateEnv.fs.path(recordDir)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("record or update staging remains", err)
	}
}

func TestUpdateUndoRetirementContinuesAfterSaveFailure(t *testing.T) {
	for _, boundary := range []string{"intent", "files-removed"} {
		t.Run(boundary, func(t *testing.T) { updateUndoSaveFailure(t, boundary) })
	}
}

func updateUndoSaveFailure(t *testing.T, boundary string) {
	t.Helper()
	f := updateUndoTailFixture(t)
	id := f.h.r.LastUpdate
	before := f.saves
	f.failSave = before + 1
	if boundary == "files-removed" {
		f.failSave++
	}
	if err := f.h.removeEnrollmentRecord(t.Context()); err == nil {
		t.Fatal("injected save failure did not stop retirement")
	}
	if _, err := os.Lstat(f.h.updateEnv.fs.path(recordDir + "/record.json")); err != nil {
		t.Fatal("record removed before staging retirement", err)
	}
	f.restart(t)
	if err := f.h.removeEnrollmentRecord(t.Context()); err != nil {
		t.Fatal("retirement did not resume", boundary, id, err)
	}
}

func TestUpdateUndoRefusesUnownedStaging(t *testing.T) {
	for _, name := range []string{"foreign-file", "foreign-stage"} {
		t.Run(name, func(t *testing.T) { updateUndoUnowned(t, name) })
	}
}

func updateUndoUnowned(t *testing.T, name string) {
	t.Helper()
	f := updateUndoTailFixture(t)
	path := updateStagePath(f.h.r.LastUpdate) + "/foreign-file"
	if name == "foreign-stage" {
		path = recordDir + "/update/foreign-stage"
	}
	f.write(t, path, []byte("preserve"), 0600)
	if err := f.h.removeEnrollmentRecord(t.Context()); err == nil {
		t.Fatal("unowned retained data was removed")
	}
	for _, preserved := range []string{path, recordDir + "/record.json", recordDir + "/lock"} {
		if _, err := os.Lstat(f.h.updateEnv.fs.path(preserved)); err != nil {
			t.Fatal("retirement lost unowned data or its record", filepath.Base(preserved), err)
		}
	}
}

func TestUpdateUndoRetirementRefusesChangedBoundFile(t *testing.T) {
	f := updateUndoTailFixture(t)
	base := f.h.updateEnv.save
	once := false
	f.h.updateEnv.save = func(record hostRecord) error {
		if !once && record.UndoUpdate != nil {
			once = true
			return errors.Join(base(record), errors.New("fixture lost save reply"))
		}
		return base(record)
	}
	if err := f.h.removeEnrollmentRecord(t.Context()); err == nil || !once {
		t.Fatal("retirement intent did not persist", err)
	}
	f.restart(t)
	path := updateStagePath(f.h.r.LastUpdate) + "/updater"
	f.write(t, path, []byte("changed"), 0600)
	if err := f.h.removeEnrollmentRecord(t.Context()); err == nil {
		t.Fatal("changed bound retirement file accepted")
	}
	if _, err := os.Lstat(f.h.updateEnv.fs.path(recordDir + "/record.json")); err != nil {
		t.Fatal("drift discarded its owning record", err)
	}
}

func TestUpdateUndoRetiresCommittedAndAbortedStaging(t *testing.T) {
	f := newUpdateFixture(t)
	plan := fixtureUpdatePlan(t, f)
	if _, err := f.h.applyUpdate(t.Context(), plan); err != nil {
		t.Fatal(err)
	}
	committed := f.h.r.LastUpdate
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
	f.h.r.Update.PreviousUpdate = committed
	f.h.r.Update.PriorIntents = maps.Clone(f.h.r.Journal.Intents)
	if err := f.h.stageUpdateArtifacts(); err != nil {
		t.Fatal(err)
	}
	request := HostRequest{Action: "update-abort", HostKey: plan.HostKey, Confirmed: plan.Digest}
	if _, err := f.h.continueUpdate(t.Context(), request); err != nil {
		t.Fatal(err)
	}
	if f.h.r.LastUpdate != committed || f.h.r.AbortedUpdate == "" {
		t.Fatal("fixture did not retain both stages")
	}
	f.h.r.Journal.Phase = Removed
	if err := f.h.updateSave(); err != nil {
		t.Fatal(err)
	}
	f.write(t, recordDir+"/record.json", f.saved, 0600)
	f.write(t, recordDir+"/lock", nil, 0600)
	if err := f.h.removeEnrollmentRecord(t.Context()); err != nil {
		t.Fatal("both retained stages were not retired", err)
	}
	if _, err := os.Lstat(f.h.updateEnv.fs.path(recordDir)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("retained staging remains", err)
	}
}
