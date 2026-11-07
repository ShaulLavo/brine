//go:build linux

package enroll

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"github.com/ShaulLavo/brine/internal/admission"
	"github.com/ShaulLavo/brine/internal/caddy"
	"github.com/ShaulLavo/brine/internal/store"
)

func (h *host) updateOperation(ctx context.Context, req HostRequest) (any, error) {
	if h.r.ID == "" || h.r.Journal.Phase != Enrolled {
		return nil, errors.New("update requires completed enrollment")
	}
	if err := permissionIdentity(PermissionsRequest{IdentityKey: req.IdentityKey, HostKey: req.HostKey}); err != nil {
		return nil, err
	}
	if err := h.loadUpdater(ctx); err != nil {
		return nil, err
	}
	h.updateEnv = h.productionUpdateEnvironment()
	if h.r.Update != nil {
		return h.continueUpdate(ctx, req)
	}
	if req.Action == "update-abort" {
		return nil, errors.New("no pending update")
	}
	plan, err := h.makeUpdatePlan(ctx, req)
	if err != nil {
		return nil, err
	}
	if req.Action == "update-plan" {
		return plan, nil
	}
	if plan.Digest != req.Confirmed {
		return nil, errors.New("plan_stale")
	}
	return h.applyUpdate(ctx, plan)
}
func (h *host) loadUpdater(ctx context.Context) error {
	path, err := os.Executable()
	if err != nil {
		return err
	}
	h.source, err = boundedRead(path)
	return err
}
func (h *host) candidateVersion(ctx context.Context) (string, error) {
	path, err := os.Executable()
	if err != nil {
		return "", err
	}
	out, err := h.run(ctx, false, path, "version", "--json")
	if err != nil || out.Truncated {
		return "", errors.Join(errors.New("candidate version unavailable"), err)
	}
	var result struct {
		Data struct {
			Version string `json:"version"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(out.Stdout), &result); err != nil || result.Data.Version == "" {
		return "", errors.New("candidate version unavailable")
	}
	return result.Data.Version, nil
}
func (h *host) makeUpdatePlan(ctx context.Context, req HostRequest) (UpdatePlan, error) {
	if h.updateEnv == nil {
		h.updateEnv = h.productionUpdateEnvironment()
	}
	if err := h.requireFreshAdmission(ctx, true); err != nil {
		return UpdatePlan{}, err
	}
	facts, err := h.facts(ctx, req.IdentityKey)
	if err != nil {
		return UpdatePlan{}, err
	}
	ordinary, err := MakePlan(facts)
	if err != nil || len(ordinary.MissingPackages) != 0 {
		return UpdatePlan{}, errors.Join(errors.New("package_requirement_changed"), err)
	}
	if _, err := (diskOperatorPolicy{h}).Read(); err != nil {
		return UpdatePlan{}, err
	}
	if err := h.qualifyUpdatePins(ctx); err != nil {
		return UpdatePlan{}, err
	}
	version, err := h.candidateVersion(ctx)
	if err != nil {
		return UpdatePlan{}, err
	}
	plan := UpdatePlan{EnrollmentID: h.r.ID, HostKey: req.HostKey, CandidateSHA256: hash(h.source), CandidateVersion: version, RecordedBinarySHA256: h.r.BinaryHash, Quiescence: admission.Store, Bootstrap: h.r.Admission == nil, SchemaTo: store.SchemaVersion, Artifacts: []UpdateArtifact{}, StaleProcesses: []string{}, DrainBlockers: []string{}}
	if req.Quiesce != "" && req.Quiesce != "store" && req.Quiesce != "runner" {
		return plan, errors.New("invalid quiescence")
	}
	if plan.Bootstrap || req.Quiesce == "runner" {
		plan.Quiescence = admission.Runner
	}
	if err := h.planUpdateArtifacts(&plan, req.ReplaceUnrecordedBinary); err != nil {
		return plan, err
	}
	capture, factsSchema, err := h.inspectUpdateStore(ctx)
	if err != nil {
		return plan, err
	}
	plan.ControlAbsent, plan.SchemaFrom = capture.Absent, factsSchema.Version
	plan.Migration = !capture.Absent && factsSchema.Version < store.SchemaVersion
	plan.StaleProcesses, plan.DrainBlockers, err = h.updateProcesses(ctx)
	if err != nil {
		return plan, err
	}
	if len(plan.DrainBlockers) > 0 && plan.Quiescence != admission.Runner {
		return plan, errors.New("drain_blockers: use --quiesce=runner; all apps stop")
	}
	plan.Digest = updateDigest(plan)
	return plan, nil
}
func (h *host) planUpdateArtifacts(plan *UpdatePlan, override string) error {
	registry := enrollmentArtifacts()
	for id, artifact := range registry {
		if artifact.class == artifactOperatorOwned || artifact.class == artifactAdmission {
			continue
		}
		entry, err := h.planUpdateArtifact(enrollmentArtifactID(id), artifact, override)
		if err != nil {
			return err
		}
		plan.Artifacts = append(plan.Artifacts, entry)
		changed := entry.Action != "unchanged"
		plan.DaemonReload = plan.DaemonReload || changed && (enrollmentArtifactID(id) == artifactObserverUnit || enrollmentArtifactID(id) == artifactAdmissionSettle)
		plan.SettleEnable = plan.SettleEnable || entry.Action == "add" && enrollmentArtifactID(id) == artifactAdmissionSettle
		plan.ObserverRestart = plan.ObserverRestart || changed && (enrollmentArtifactID(id) == artifactBinary || enrollmentArtifactID(id) == artifactObserverUnit)
		plan.UserDaemonReload = plan.UserDaemonReload || changed && (enrollmentArtifactID(id) == artifactReconcileUnit || enrollmentArtifactID(id) == artifactReconcileGenerator)
	}
	return nil
}
func updateAddable(id enrollmentArtifactID) bool {
	return id == artifactObserverUnit || id == artifactReconcileUnit || id == artifactReconcileGenerator || id == artifactAdmissionSettle
}
func (h *host) planUpdateArtifact(id enrollmentArtifactID, a enrollmentArtifact, override string) (UpdateArtifact, error) {
	fs := h.updateEnv.fs
	observed, _, err := fs.read(a.path)
	entry := UpdateArtifact{Path: a.path, Class: string(a.class), Action: "unchanged", Before: observed, After: observed}
	if err != nil {
		return entry, err
	}
	old, recorded := h.r.Files[a.path]
	if id == artifactBinary && override != "" && override != observed.SHA256 {
		return entry, errors.New("installed_artifact_drift")
	}
	if observed.Present && (observed.UID != fs.owner || observed.Mode != uint32(a.mode)) {
		return entry, updateError("installed_artifact_drift", a.path)
	}
	code := "unrecorded_artifact"
	if a.class == artifactToolPin {
		code = "tool_pin_changed"
	}
	if !recorded && (!updateAddable(id) || observed.Present) {
		return entry, updateError(code, a.path)
	}
	if !recorded {
		if err := h.requireUpdateArtifactParent(a); err != nil {
			return entry, err
		}
		entry.Action = "add"
	} else if !observed.Present || observed.SHA256 != old.Hash {
		allowed := id == artifactBinary && observed.Present && (observed.SHA256 == hash(h.source) || override == observed.SHA256)
		if !allowed {
			return entry, updateError("installed_artifact_drift", a.path)
		}
	}
	if recorded && id == artifactBinary && old.Hash != observed.SHA256 && entry.Action == "unchanged" {
		entry.Action = "replace"
	}
	if id == artifactCaddyImport {
		if err := h.updateCaddyAuthority(old); err != nil {
			return entry, err
		}
	}
	if a.render == nil {
		return entry, nil
	}
	to := hash(a.render(h))
	if a.class != artifactUpdatable && to != old.Hash {
		return entry, updateError("authority_artifact_changed", a.path)
	}
	if a.class == artifactUpdatable && observed.SHA256 != to {
		if entry.Action != "add" {
			entry.Action = "replace"
		}
		entry.After = UpdateFile{Present: true, SHA256: to, Size: int64(len(a.render(h))), Mode: uint32(a.mode), UID: fs.owner, GID: os.Getegid()}
	}
	return entry, nil
}
func (h *host) inspectUpdateStore(ctx context.Context) (store.Capture, store.SchemaFacts, error) {
	fs := h.updateEnv.fs
	capture, err := store.CaptureBytes(ctx, fs.path("/"+admissionState), h.r.UID)
	if err != nil {
		return capture, store.SchemaFacts{}, errors.Join(errors.New("control_snapshot_unavailable"), err)
	}
	facts, err := store.InspectCapture(ctx, capture, fs.path(recordDir))
	if err != nil || !facts.Absent && (!facts.Integrity || !facts.ForeignKeys) {
		return capture, facts, errors.Join(errors.New("control_snapshot_unavailable"), err)
	}
	if facts.Version > store.SchemaVersion {
		return capture, facts, errors.New("schema_newer_than_candidate")
	}
	return capture, facts, nil
}
func (h *host) updateProcesses(ctx context.Context) ([]string, []string, error) {
	root := h.updateEnv.fs.path("/proc")
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil, nil, err
	}
	if len(entries) > 65536 {
		return nil, nil, errors.New("process observation limit")
	}
	stale, blockers := []string{}, []string{}
	for _, entry := range entries {
		if _, err := strconv.Atoi(entry.Name()); err != nil {
			continue
		}
		unit, blocked, err := h.planUpdateProcess(ctx, root, entry.Name())
		if err != nil {
			return nil, nil, err
		}
		if unit != "" {
			stale = append(stale, unit)
		}
		if blocked {
			blockers = append(blockers, unit)
		}
	}
	return stale, blockers, nil
}
func (h *host) updateProcess(ctx context.Context, root, pid string) (string, error) {
	path := filepath.Join(root, pid)
	info, err := os.Stat(path)
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	// Only runner processes and the fixed observer can execute installed code.
	if int(info.Sys().(*syscall.Stat_t).Uid) != h.r.UID && int(info.Sys().(*syscall.Stat_t).Uid) != 0 {
		return "", nil
	}
	exe, err := os.Readlink(filepath.Join(path, "exe"))
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	if strings.TrimSuffix(exe, " (deleted)") != binaryPath {
		return "", nil
	}
	data, err := proofRead(ctx, filepath.Join(path, "cgroup"), 65536)
	if err != nil {
		return "", err
	}
	unit := filepath.Base(strings.TrimSpace(string(data)))
	if strings.HasPrefix(unit, "brine-") && strings.HasSuffix(unit, ".service") {
		return unit, nil
	}
	return "", errors.New("unclassified installed Brine process")
}

func (h *host) qualifyUpdatePins(ctx context.Context) error {
	for _, asset := range []struct {
		code  string
		check func(context.Context) (bool, error)
	}{{"tool_pin_changed", h.checkLitestream}, {"tool_pin_changed", h.checkRestic}, {"tool_pin_changed", h.checkNativeReader}, {"runner_storage_changed", h.checkRunnerStorage}} {
		ok, err := asset.check(ctx)
		if err != nil || !ok {
			return errors.Join(errors.New(asset.code), err)
		}
	}
	return nil
}
func (h *host) updateCaddyAuthority(old ownedFile) error {
	input := old.Before
	for _, pkg := range h.r.Packages {
		if strings.Split(pkg.Name, ":")[0] == "caddy" {
			input = []byte("# Brine-owned root configuration; no package default site.\n")
		}
	}
	data, err := caddy.EnrollmentRoot(input)
	if err != nil || hash(data) != old.Hash {
		return errors.Join(errors.New("authority_artifact_changed"), err)
	}
	return nil
}

func (h *host) planUpdateProcess(ctx context.Context, root, pid string) (string, bool, error) {
	unit, err := h.updateProcess(ctx, root, pid)
	if err != nil || !strings.HasPrefix(unit, "brine-engine-") {
		return unit, false, err
	}
	blocked, err := h.updateEngineBlocker(ctx, root, pid)
	return unit, blocked, err
}
func (h *host) updateEngineBlocker(ctx context.Context, procRoot, pid string) (bool, error) {
	p := AdmissionProofs{Root: h.updateEnv.fs.root, ProcRoot: procRoot}.bounded()
	current, err := p.storeView(h.r.UID)
	if err != nil {
		return false, err
	}
	process, _, err := p.scanProcess(ctx, pid, admissionStoreView{}, -1, p.limits.fds)
	if err != nil {
		return false, err
	}
	if updateFDReferences(process.fds, current.files) {
		return true, nil
	}
	for _, task := range process.tasks {
		if updateFDReferences(task.fds, current.files) {
			return true, nil
		}
	}
	return false, nil
}
func updateFDReferences(fds map[string]admissionFDView, files map[string]admissionFileIdentity) bool {
	ids := map[admissionFileIdentity]bool{}
	for _, id := range files {
		ids[id] = true
	}
	for _, fd := range fds {
		if ids[fd.identity] {
			return true
		}
	}
	return false
}

func (h *host) requireUpdateArtifactParent(a enrollmentArtifact) error {
	parent, err := h.updateEnv.fs.directory(filepath.Dir(a.path), h.updateEnv.fs.owner)
	if err != nil {
		return errors.Join(updateError("artifact_parent_unavailable", a.path), err)
	}
	return parent.Close()
}
