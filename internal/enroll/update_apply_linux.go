//go:build linux

package enroll

import (
	"context"
	"encoding/json"
	"errors"
	"maps"
	"os"

	"github.com/ShaulLavo/brine/internal/admission"
	"github.com/ShaulLavo/brine/internal/store"
)

func (h *host) applyUpdate(ctx context.Context, plan UpdatePlan) (value any, err error) {
	u, err := newUpdateRecord(plan)
	if err != nil {
		return nil, err
	}
	u.PreviousUpdate = h.r.LastUpdate
	u.PreviousAborted = h.r.AbortedUpdate
	u.PriorIntents = maps.Clone(h.r.Journal.Intents)
	h.r.Update = u
	if plan.Bootstrap {
		if err := h.updateSave(); err != nil {
			return nil, err
		}
		if err := h.stageUpdateArtifacts(); err != nil {
			return nil, err
		}
		if err := h.fenceRunner(ctx); err != nil {
			return nil, err
		}
		if err := h.installAdmission(ctx); err != nil {
			return nil, err
		}
		u.AdmissionInstalled = true
		if err := h.updateSave(); err != nil {
			return nil, err
		}
	}
	d, err := h.updateEnv.drain(ctx, h.updatePending(), !plan.Bootstrap)
	if err != nil {
		return nil, err
	}
	defer func() { err = errors.Join(err, d.Close()) }()
	if !plan.Bootstrap {
		if err := h.updateSave(); err != nil {
			return nil, err
		}
	}
	return h.runUpdate(ctx, d)
}
func (h *host) continueUpdate(ctx context.Context, req HostRequest) (value any, err error) {
	u := h.r.Update
	if hash(h.source) != u.Plan.CandidateSHA256 {
		return nil, errors.New("update_pending_different_candidate")
	}
	if !proofID(u.ID) || len(u.Plan.Artifacts) == 0 || len(u.Artifacts) != len(u.Plan.Artifacts) {
		return nil, admission.ErrInconsistent
	}
	if updateDigest(u.Plan) != u.Plan.Digest || u.Plan.EnrollmentID != h.r.ID || u.Plan.HostKey != req.HostKey {
		return nil, admission.ErrInconsistent
	}
	if req.Action == "update-plan" {
		return u.Plan, nil
	}
	if req.Confirmed != u.Plan.Digest {
		return nil, errors.New("plan_stale")
	}
	if u.Stage == admission.Terminal {
		if req.Action == "update-abort" && !u.Aborting {
			return nil, errors.New("update_already_committing: resume --update to finish reopening")
		}
		if err := h.resumeUpdateTerminal(ctx); err != nil {
			return nil, err
		}
		return updateOutcome(u), nil
	}
	if u.Plan.Bootstrap && h.r.Admission == nil {
		if err := h.stageUpdateArtifacts(); err != nil {
			return nil, err
		}
		if err := h.fenceRunner(ctx); err != nil {
			return nil, err
		}
		if err := h.installAdmission(ctx); err != nil {
			return nil, err
		}
		u.AdmissionInstalled = true
		if err := h.updateSave(); err != nil {
			return nil, err
		}
	}
	if err := h.bindAdmission(); err != nil {
		return nil, err
	}
	state, err := h.updateEnv.state(ctx)
	if err != nil {
		return nil, err
	}
	if state.Open || state.Pending.Kind != admission.Update || state.Pending.ID != u.ID {
		return nil, admission.ErrInconsistent
	}
	d, err := h.updateEnv.drain(ctx, h.updatePending(), false)
	if err != nil {
		return nil, err
	}
	defer func() { err = errors.Join(err, d.Close()) }()
	if err := d.RevokeAndDrain(ctx); err != nil {
		return nil, err
	}
	if req.Action == "update-abort" && u.Plan.Artifacts[0].Before.SHA256 != u.Plan.RecordedBinarySHA256 {
		return nil, errors.New("unrecorded_binary_rollback_unsupported")
	}
	if req.Action == "update-abort" {
		if err := h.settleActivationBeforeAbort(ctx); err != nil {
			return nil, err
		}
	}
	if req.Action == "update-abort" {
		u.Aborting = true
		if err := h.updateSave(); err != nil {
			return nil, err
		}
	}
	if err := h.stageUpdateArtifacts(); err != nil {
		return nil, err
	}
	if err := h.retainedUpdater(); err != nil {
		return nil, err
	}
	return h.runUpdate(ctx, d)
}
func updateOutcome(u *updateRecord) UpdateResult {
	result := UpdateResult{ID: u.ID, Committed: !u.Aborting, Aborted: u.Aborting, Retained: updateStagePath(u.ID)}
	if u.Raw != nil {
		result.Quarantine = "/" + admissionState + "/quarantine-" + u.ID
	}
	return result
}
func (h *host) updateStage(ctx context.Context, d updateDrain, stage admission.Stage) error {
	if err := d.SetStage(ctx, stage); err != nil {
		return err
	}
	h.r.Update.Stage = stage
	return h.updateSave()
}
func (h *host) runUpdate(ctx context.Context, d updateDrain) (any, error) {
	u := h.r.Update
	if u.Aborting {
		return h.abortUpdate(ctx, d)
	}
	if u.Plan.Quiescence == admission.Runner {
		if err := h.fenceRunner(ctx); err != nil {
			return nil, err
		}
	}
	if err := h.stageUpdateArtifacts(); err != nil {
		return nil, err
	}
	if u.Capture == nil {
		if err := h.captureUpdate(ctx, d); err != nil {
			return nil, err
		}
	}
	if err := h.cleanupUpdateTemporaries(); err != nil {
		return nil, err
	}
	if err := h.replaceUpdateArtifacts(ctx, d, false); err != nil {
		return nil, err
	}
	if err := h.updateStage(ctx, d, admission.Activating); err != nil {
		return nil, err
	}
	if err := h.updateProof(ctx); err != nil {
		return nil, err
	}
	if err := h.activateUpdate(ctx, &u.Activation, false); err != nil {
		return nil, err
	}
	if u.Plan.Migration {
		if err := h.migrateUpdate(ctx, d); err != nil {
			return nil, err
		}
	}
	if err := h.finishUpdate(ctx, d); err != nil {
		return nil, err
	}
	return updateOutcome(u), nil
}
func (h *host) stageUpdateArtifacts() error {
	fs := h.updateEnv.fs
	if err := fs.mkdir(recordDir+"/update", 0700, fs.owner, os.Getegid()); err != nil {
		return err
	}
	u := h.r.Update
	if err := fs.mkdir(updateStagePath(u.ID), 0700, fs.owner, os.Getegid()); err != nil {
		return err
	}
	if err := h.stagePayload(updateStagePath(u.ID)+"/updater", h.source); err != nil {
		return err
	}
	manifest, err := json.Marshal(u.Plan)
	if err != nil {
		return err
	}
	if err := h.stagePayload(updateStagePath(u.ID)+"/manifest.json", manifest); err != nil {
		return err
	}
	for index, item := range u.Plan.Artifacts {
		if err := h.stageUpdateArtifact(index, item); err != nil {
			return err
		}
	}
	return nil
}
func (h *host) stageUpdateArtifact(index int, item UpdateArtifact) error {
	if item.Action == "unchanged" {
		return nil
	}
	fs := h.updateEnv.fs
	u := h.r.Update
	observed, data, err := fs.read(item.Path)
	if err != nil {
		return err
	}
	receipt := &u.Artifacts[index]
	if receipt.Done || receipt.Undone {
		return nil
	}
	before, _, err := fs.read(stageArtifact(u.ID, index, "before"))
	if err != nil {
		return err
	}
	if before.Present && (before.SHA256 != item.Before.SHA256 || before.Mode != 0600 || before.UID != fs.owner) {
		return errors.New("update_staging_drift")
	}
	if item.Before.Present && !before.Present {
		if !updateSame(observed, item.Before) {
			return updateError("update_drift", item.Path)
		}
		if err := h.stagePayload(stageArtifact(u.ID, index, "before"), data); err != nil {
			return err
		}
	}
	artifact, ok := findUpdateArtifact(item.Path)
	if !ok || artifact.render == nil {
		return errors.New("update manifest contains unregistered artifact")
	}
	if err := h.stagePayload(stageArtifact(u.ID, index, "after"), artifact.render(h)); err != nil {
		return err
	}
	if receipt.Previous == nil {
		if previous, ok := h.r.Files[item.Path]; ok {
			receipt.Previous = &previous
		}
		return h.updateSave()
	}
	return nil
}
func findUpdateArtifact(path string) (enrollmentArtifact, bool) {
	for _, a := range enrollmentArtifacts() {
		if a.path == path {
			return a, true
		}
	}
	return enrollmentArtifact{}, false
}
func (h *host) captureUpdate(ctx context.Context, d updateDrain) error {
	if err := h.updateProof(ctx); err != nil {
		return err
	}
	capture, facts, err := h.inspectUpdateStore(ctx)
	if err != nil {
		return err
	}
	plan := h.r.Update.Plan
	if capture.Absent != plan.ControlAbsent || facts.Version != plan.SchemaFrom {
		return h.rejectUnchangedUpdate(ctx, d, "plan_stale")
	}
	if plan.Migration {
		_, facts, err = store.RehearseMigration(ctx, capture, h.updateEnv.fs.path(updateStagePath(h.r.Update.ID)))
		if err != nil || facts.Version != plan.SchemaTo || !facts.Integrity || !facts.ForeignKeys {
			return h.rejectUnchangedUpdate(ctx, d, "migration_rehearsal_failed")
		}
	}
	if !capture.Absent {
		if err := h.stagePayload(updateStagePath(h.r.Update.ID)+"/capture.db", capture.DB); err != nil {
			return err
		}
	}
	if capture.WAL != nil {
		if err := h.stagePayload(updateStagePath(h.r.Update.ID)+"/capture.wal", capture.WAL); err != nil {
			return err
		}
	}
	h.r.Update.Capture = &updateCapture{Absent: capture.Absent, WALPresent: capture.WAL != nil, DB: capture.DBID, WAL: capture.WALID, Digest: captureDigest(capture)}
	return h.updateStage(ctx, d, admission.Captured)
}
func (h *host) rejectUnchangedUpdate(ctx context.Context, d updateDrain, reason string) error {
	h.r.Update.Aborting = true
	if err := h.updateSave(); err != nil {
		return err
	}
	return errors.Join(errors.New(reason), h.finishUpdate(ctx, d))
}
func (h *host) replaceUpdateArtifacts(ctx context.Context, d updateDrain, abort bool) error {
	if err := h.updateProof(ctx); err != nil {
		return err
	}
	if err := h.updateStage(ctx, d, admission.Replacing); err != nil {
		return err
	}
	// New admission and its settle unit must exist before a gated binary.
	for _, settle := range []bool{true, false} {
		if err := h.replaceUpdateGroup(ctx, settle, abort); err != nil {
			return err
		}
	}
	return nil
}
func (h *host) replaceUpdateGroup(ctx context.Context, settle, abort bool) error {
	for index, item := range h.r.Update.Plan.Artifacts {
		if (item.Path == settleUnitPath) != settle {
			continue
		}
		if err := h.proveUpdateArtifact(ctx, item); err != nil {
			return err
		}
		if err := h.replaceUpdateArtifact(index, item, abort); err != nil {
			return err
		}
	}
	return nil
}
func (h *host) replaceUpdateArtifact(index int, item UpdateArtifact, abort bool) error {
	fs := h.updateEnv.fs
	observed, _, err := fs.read(item.Path)
	if err != nil {
		return err
	}
	receipt := &h.r.Update.Artifacts[index]
	if item.Action == "unchanged" {
		if !updateSame(observed, item.Before) {
			return updateError("update_drift", item.Path)
		}
		return nil
	}
	if abort {
		return h.restoreUpdateArtifact(index, item, observed)
	}
	if receipt.Installed != nil && !updateSame(observed, *receipt.Installed) {
		return updateError("update_drift", item.Path)
	}
	if !updateSame(observed, item.Before) && !updateEquivalent(observed, item.After) {
		return updateError("update_drift", item.Path)
	}
	if !updateEquivalent(observed, item.After) {
		_, data, err := fs.read(stageArtifact(h.r.Update.ID, index, "after"))
		if err != nil || hash(data) != item.After.SHA256 {
			return errors.Join(errors.New("update_staging_drift"), err)
		}
		observed, err = fs.write(item.Path, data, item.After.Mode, item.After.UID, item.After.GID)
		if err != nil {
			return err
		}
	}
	receipt.Installed = &observed
	receipt.Done = true
	old := h.r.Files[item.Path]
	old.Hash, old.Mode = item.After.SHA256, item.After.Mode
	h.r.Files[item.Path] = old
	if item.Action == "add" {
		h.updateAddedIntent(item.Path, true)
	}
	return h.updateSave()
}
func (h *host) updateAddedIntent(path string, enabled bool) {
	step := "boot-reconcile"
	if path == settleUnitPath {
		step = "admission-settle"
	}
	if path == "/etc/systemd/system/brine-workspace-observer.service" {
		step = "workspace-observer"
	}
	if h.r.Journal.Intents == nil {
		h.r.Journal.Intents = map[string]bool{}
	}
	if enabled {
		h.r.Journal.Intents[step] = true
	} else {
		delete(h.r.Journal.Intents, step)
	}
}
func (h *host) migrateUpdate(ctx context.Context, d updateDrain) error {
	if err := d.RevokeAndDrain(ctx); err != nil {
		return err
	}
	if err := h.updateProof(ctx); err != nil {
		return err
	}
	capture, facts, err := h.inspectUpdateStore(ctx)
	if err != nil {
		return err
	}
	u := h.r.Update
	if facts.Version == u.Plan.SchemaTo {
		u.MigratedSHA256 = captureDigest(capture)
		return h.updateSave()
	}
	if facts.Version != u.Plan.SchemaFrom {
		return errors.New("migration_schema_drift")
	}
	if err := h.updateStage(ctx, d, admission.Migrating); err != nil {
		return err
	}
	e, err := d.EnableStage(ctx, admission.Migrating)
	if err != nil {
		return err
	}
	_, childErr := h.run(ctx, true, "runuser", "-u", "brine", "--", binaryPath, "host", "control-migrate", "update", e.ID, "migrating", e.Attempt)
	if err := d.RevokeAndDrain(ctx); err != nil {
		return errors.Join(childErr, err)
	}
	if err := h.updateProof(ctx); err != nil {
		return errors.Join(childErr, err)
	}
	capture, facts, err = h.inspectUpdateStore(ctx)
	if err != nil || facts.Version != u.Plan.SchemaTo {
		return errors.Join(errors.New("migration_pending"), childErr, err)
	}
	u.MigratedSHA256 = captureDigest(capture)
	return h.updateSave()
}
func (h *host) loadUpdateCapture() (store.Capture, error) {
	u := h.r.Update
	if u.Capture == nil {
		return store.Capture{}, errors.New("capture unavailable")
	}
	c := store.Capture{Absent: u.Capture.Absent, DBID: u.Capture.DB, WALID: u.Capture.WAL}
	fs := h.updateEnv.fs
	var err error
	if !c.Absent {
		_, c.DB, err = fs.read(updateStagePath(u.ID) + "/capture.db")
		if err != nil {
			return c, err
		}
	}
	if u.Capture.WALPresent {
		_, c.WAL, err = fs.read(updateStagePath(u.ID) + "/capture.wal")
		if err != nil {
			return c, err
		}
	}
	if captureDigest(c) != u.Capture.Digest || !c.Absent && hash(c.DB) != c.DBID.SHA256 || c.WAL != nil && hash(c.WAL) != c.WALID.SHA256 {
		return c, errors.New("capture staging drift")
	}
	return c, nil
}
func (h *host) abortUpdate(ctx context.Context, d updateDrain) (any, error) {
	u := h.r.Update
	if u.Plan.Artifacts[0].Before.SHA256 != u.Plan.RecordedBinarySHA256 {
		return nil, errors.New("unrecorded_binary_rollback_unsupported")
	}
	if err := h.retainedUpdater(); err != nil {
		return nil, err
	}
	if err := d.RevokeAndDrain(ctx); err != nil {
		return nil, err
	}
	if err := h.updateProof(ctx); err != nil {
		return nil, err
	}
	if u.Capture != nil {
		capture, err := h.loadUpdateCapture()
		if err != nil {
			return nil, err
		}
		if err := h.updateStage(ctx, d, admission.Quarantining); err != nil {
			return nil, err
		}
		if err := h.restoreUpdateCapture(ctx, capture); err != nil {
			return nil, err
		}
	}
	if err := h.disableAddedSettle(ctx); err != nil {
		return nil, err
	}
	if err := h.removeAddedUpdateObserver(ctx); err != nil {
		return nil, err
	}
	if err := h.replaceUpdateArtifacts(ctx, d, true); err != nil {
		return nil, err
	}
	if err := h.activateUpdate(ctx, &u.RollbackActivation, true); err != nil {
		return nil, err
	}
	if err := h.finishUpdate(ctx, d); err != nil {
		return nil, err
	}
	return updateOutcome(u), nil
}
func (h *host) restoreUpdateArtifact(index int, item UpdateArtifact, observed UpdateFile) error {
	u := h.r.Update
	r := &u.Artifacts[index]
	if r.Restored != nil && updateSame(observed, *r.Restored) {
		return nil
	}
	if !updateEquivalent(observed, item.After) && !updateEquivalent(observed, item.Before) {
		return updateError("update_drift", item.Path)
	}
	if r.Installed != nil && !updateSame(observed, *r.Installed) && !updateEquivalent(observed, item.Before) {
		return updateError("update_drift", item.Path)
	}
	fs := h.updateEnv.fs
	if !item.Before.Present {
		if err := h.recordUpdateRemovalIdentity(r, observed); err != nil {
			return err
		}
		if err := fs.remove(item.Path); err != nil {
			return err
		}
		delete(h.r.Files, item.Path)
		h.updateAddedIntent(item.Path, false)
		r.Undone = true
		return h.updateSave()
	}
	_, data, err := fs.read(stageArtifact(u.ID, index, "before"))
	if err != nil || hash(data) != item.Before.SHA256 {
		return errors.Join(errors.New("update_staging_drift"), err)
	}
	if !updateEquivalent(observed, item.Before) {
		observed, err = fs.write(item.Path, data, item.Before.Mode, item.Before.UID, item.Before.GID)
		if err != nil {
			return err
		}
	}
	r.Restored = &observed
	r.Undone = true
	if r.Previous == nil {
		return errors.New("missing prior enrollment record")
	}
	h.r.Files[item.Path] = *r.Previous
	return h.updateSave()
}

func (h *host) proveUpdateArtifact(ctx context.Context, item UpdateArtifact) error {
	if item.Action == "unchanged" {
		return nil
	}
	return h.updateProof(ctx)
}

func (h *host) recordUpdateRemovalIdentity(r *updateArtifactReceipt, observed UpdateFile) error {
	if !observed.Present || r.Installed != nil {
		return nil
	}
	r.Installed = &observed
	return h.updateSave()
}
