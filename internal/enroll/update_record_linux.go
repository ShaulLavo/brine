//go:build linux

package enroll

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"time"

	"github.com/ShaulLavo/brine/internal/admission"
	"github.com/ShaulLavo/brine/internal/store"
)

type updateRecord struct {
	ID                 string                  `json:"id"`
	Plan               UpdatePlan              `json:"plan"`
	Stage              admission.Stage         `json:"stage"`
	Since              string                  `json:"since"`
	Aborting           bool                    `json:"aborting,omitempty"`
	Capture            *updateCapture          `json:"capture,omitempty"`
	Artifacts          []updateArtifactReceipt `json:"artifacts"`
	Fence              *updateRunnerFence      `json:"fence,omitempty"`
	Activation         updateActivation        `json:"activation"`
	RollbackActivation updateActivation        `json:"rollback_activation"`
	Raw                *rawRollback            `json:"raw,omitempty"`
	MigratedSHA256     string                  `json:"migrated_sha256,omitempty"`
	Reopening          *updateReopening        `json:"reopening,omitempty"`
	RemovalPending     bool                    `json:"removal_pending,omitempty"`
	RemovedAdmission   [3]bool                 `json:"removed_admission"`
	PreviousAborted    string                  `json:"previous_aborted,omitempty"`
	PreviousUpdate     string                  `json:"previous_update,omitempty"`
	Retired            *updateRetiredStage     `json:"retired,omitempty"`
	PriorIntents       map[string]bool         `json:"prior_intents"`
	AdmissionInstalled bool                    `json:"admission_installed,omitempty"`
}

type updateArtifactReceipt struct {
	Previous  *ownedFile  `json:"previous,omitempty"`
	Installed *UpdateFile `json:"installed,omitempty"`
	Restored  *UpdateFile `json:"restored,omitempty"`
	Done      bool        `json:"done,omitempty"`
	Undone    bool        `json:"undone,omitempty"`
}
type updateCapture struct {
	Absent     bool `json:"absent"`
	WALPresent bool `json:"wal_present"`
	DB, WAL    store.FileID
	Digest     string `json:"digest"`
}
type updateRunnerFence struct {
	Expiry        string `json:"expiry"`
	Linger        bool   `json:"linger"`
	ManagerActive bool   `json:"manager_active"`
	ExpireIntent  bool   `json:"expire_intent"`
	LingerIntent  bool   `json:"linger_intent"`
	StopIntent    bool   `json:"stop_intent"`
}
type updateActivation struct {
	Enable          updateEffect `json:"enable"`
	Reload          updateEffect `json:"reload"`
	Observer        updateEffect `json:"observer"`
	ObserverRemoval updateEffect `json:"observer_removal"`
	UserReload      updateEffect `json:"user_reload"`
}
type updateEffect struct {
	Intent   bool   `json:"intent"`
	Done     bool   `json:"done"`
	Before   string `json:"before,omitempty"`
	Observed string `json:"observed,omitempty"`
}
type updateReopening struct {
	ExpiryDone bool         `json:"expiry_done"`
	LingerDone bool         `json:"linger_done"`
	Manager    updateEffect `json:"manager"`
	Reconcile  updateEffect `json:"reconcile"`
}

type updateDrain interface {
	SetStage(context.Context, admission.Stage) error
	EnableStage(context.Context, admission.Stage) (admission.Exemption, error)
	RevokeAndDrain(context.Context) error
	Reopen(context.Context) error
	Close() error
}
type updateEnvironment struct {
	fs    updateFS
	save  func(hostRecord) error
	state func(context.Context) (admission.State, error)
	drain func(context.Context, admission.Pending, bool) (updateDrain, error)
	prove func(context.Context, AdmissionProofContext, bool) error
}

func updateID() (string, error) {
	var id [16]byte
	_, err := rand.Read(id[:])
	return hex.EncodeToString(id[:]), err
}
func isUpdateAction(action string) bool {
	return action == "update-plan" || action == "update-apply" || action == "update-abort"
}
func (h *host) updateSave() error {
	if h.updateEnv.save != nil {
		return h.updateEnv.save(h.r)
	}
	return h.Save(h.r.Journal)
}
func (h *host) updatePending() admission.Pending {
	u := h.r.Update
	return admission.Pending{Kind: admission.Update, ID: u.ID, Stage: admission.Admitting, Quiescence: u.Plan.Quiescence, Since: u.Since}
}
func updateDigest(plan UpdatePlan) string {
	plan.Digest = ""
	raw, _ := json.Marshal(plan)
	return hash(raw)
}
func captureDigest(c store.Capture) string {
	raw, _ := json.Marshal(updateCapture{Absent: c.Absent, WALPresent: c.WAL != nil, DB: c.DBID, WAL: c.WALID})
	return hash(raw)
}

func (h *host) updateProof(ctx context.Context) error {
	u := h.r.Update
	binding := AdmissionProofContext{EnrollmentID: h.r.ID, TransactionID: u.ID, RunnerUID: h.r.UID, FenceExpiryDays: 1}
	return h.updateEnv.prove(ctx, binding, u.Plan.Quiescence == admission.Runner)
}

func newUpdateRecord(plan UpdatePlan) (*updateRecord, error) {
	id, err := updateID()
	if err != nil {
		return nil, err
	}
	return &updateRecord{ID: id, Plan: plan, Stage: admission.Fenced, Since: time.Now().UTC().Format(time.RFC3339Nano), Artifacts: make([]updateArtifactReceipt, len(plan.Artifacts))}, nil
}

func (h *host) retainedUpdater() error {
	u := h.r.Update
	observed, _, err := h.updateEnv.fs.read(updateStagePath(u.ID) + "/updater")
	if err != nil || observed.UID != h.updateEnv.fs.owner || observed.Mode != 0600 || observed.SHA256 != u.Plan.CandidateSHA256 || hash(h.source) != u.Plan.CandidateSHA256 {
		return errors.Join(errors.New("update_pending_different_candidate"), err)
	}
	return nil
}

func (h *host) stagePayload(path string, data []byte) error {
	observed, _, err := h.updateEnv.fs.read(path)
	if err != nil {
		return err
	}
	if observed.Present {
		if observed.SHA256 != hash(data) || observed.UID != h.updateEnv.fs.owner || observed.Mode != 0600 {
			return updateError("update_staging_drift", path)
		}
		return nil
	}
	_, err = h.updateEnv.fs.write(path, data, 0600, h.updateEnv.fs.owner, os.Getegid())
	return err
}
