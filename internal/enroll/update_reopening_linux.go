//go:build linux

package enroll

import (
	"context"
	"errors"
	"golang.org/x/sys/unix"
	"maps"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/ShaulLavo/brine/internal/admission"
	"github.com/ShaulLavo/brine/internal/localexec"
)

func (h *host) finishUpdate(ctx context.Context, d updateDrain) error {
	u := h.r.Update
	if !u.Aborting {
		if err := h.verifyCandidateVersion(ctx); err != nil {
			return err
		}
	}
	if err := d.RevokeAndDrain(ctx); err != nil {
		return err
	}
	u.Reopening = &updateReopening{}
	if u.Fence == nil && h.updateReconcileRequired() {
		p, err := h.userUpdateUnit(ctx, "brine-reconcile.service")
		if err != nil {
			return err
		}
		u.Reopening.Reconcile.Before = p["InvocationID"]
	}
	if u.Fence == nil {
		u.Reopening.ExpiryDone = true
		u.Reopening.LingerDone = true
		u.Reopening.Manager.Done = true
	}
	if err := h.updateStage(ctx, d, admission.Terminal); err != nil {
		return err
	}
	if err := d.Reopen(ctx); err != nil {
		return err
	}
	return h.completeUpdateReopening(ctx)
}
func (h *host) resumeUpdateTerminal(ctx context.Context) (err error) {
	u := h.r.Update
	if u == nil || u.Stage != admission.Terminal || u.Reopening == nil {
		return admission.ErrInconsistent
	}
	if u.RemovalPending {
		return h.completeBootstrapRemoval(ctx)
	}
	if err := h.bindAdmission(); err != nil {
		return err
	}
	state, err := h.updateEnv.state(ctx)
	if err != nil {
		return err
	}
	if !state.Open {
		if state.Pending.Kind != admission.Update || state.Pending.ID != u.ID {
			return admission.ErrInconsistent
		}
		d, err := h.updateEnv.drain(ctx, h.updatePending(), false)
		if err != nil {
			return err
		}
		defer func() { err = errors.Join(err, d.Close()) }()
		if err := d.SetStage(ctx, admission.Terminal); err != nil {
			return err
		}
		if err := d.Reopen(ctx); err != nil {
			return err
		}
	}
	return h.completeUpdateReopening(ctx)
}
func (h *host) completeUpdateReopening(ctx context.Context) error {
	u := h.r.Update
	state, err := h.updateEnv.state(ctx)
	if err != nil || !state.Open {
		return errors.Join(admission.ErrInconsistent, err)
	}
	if err := h.restoreRunnerFence(ctx); err != nil {
		return err
	}
	activation := &u.Activation
	if u.Aborting {
		activation = &u.RollbackActivation
	}
	if u.Plan.UserDaemonReload && u.Fence != nil && u.Fence.ManagerActive {
		if err := h.userUpdateEffect(ctx, &activation.UserReload, "user-reload", "daemon-reload"); err != nil {
			return err
		}
	}
	if err := h.handoffUpdateReconcile(ctx); err != nil {
		return err
	}
	if err := h.pruneRecordedUpdates(ctx); err != nil {
		return err
	}
	if err := h.settleUpdateIssuers(ctx); err != nil {
		return err
	}
	if u.Aborting {
		h.r.BinaryHash = u.Plan.RecordedBinarySHA256
	} else {
		h.r.BinaryHash = u.Plan.CandidateSHA256
	}
	if u.Aborting {
		h.r.Journal.Intents = maps.Clone(u.PriorIntents)
	}
	if u.Aborting && u.Plan.Bootstrap {
		return h.removeBootstrapAdmission(ctx)
	}
	h.recordOpenAdmissionHash()
	h.finishUpdateRetention(u)
	h.r.Update = nil
	return h.updateSave()
}
func (h *host) restoreRunnerFence(ctx context.Context) error {
	u := h.r.Update
	r := u.Reopening
	f := u.Fence
	if f == nil {
		return nil
	}
	if !r.ExpiryDone {
		value := f.Expiry
		if value == "" {
			value = "-1"
		}
		if _, err := h.updateCommand(ctx, true, "usermod", "--expiredate", value, "brine"); err != nil {
			return err
		}
		shadow, err := h.updateCommand(ctx, false, "getent", "shadow", "brine")
		fields := strings.Split(strings.TrimSuffix(shadow, "\n"), ":")
		if err != nil || len(fields) != 9 || fields[7] != f.Expiry {
			return errors.Join(errors.New("account_reopening_pending"), err)
		}
		r.ExpiryDone = true
		if err := h.updateSave(); err != nil {
			return err
		}
	}
	if !r.LingerDone {
		verb := "disable-linger"
		if f.Linger {
			verb = "enable-linger"
		}
		if _, err := h.updateCommand(ctx, true, "loginctl", verb, "brine"); err != nil {
			return err
		}
		_, err := os.Lstat(h.updateEnv.fs.path("/var/lib/systemd/linger/brine"))
		if f.Linger && err != nil || !f.Linger && !errors.Is(err, os.ErrNotExist) {
			return errors.Join(errors.New("linger_reopening_pending"), err)
		}
		r.LingerDone = true
		if err := h.updateSave(); err != nil {
			return err
		}
	}
	if !f.ManagerActive {
		r.Manager.Done = true
		return h.updateSave()
	}
	return h.issueUpdateEffect(ctx, &r.Manager, "manager", "/usr/bin/systemctl", "start", h.fenceUnit())
}
func (h *host) settleUpdateIssuers(ctx context.Context) error {
	u := h.r.Update
	if err := h.retireUpdateActivation(ctx, &u.Activation, false); err != nil {
		return err
	}
	if err := h.retireUpdateActivation(ctx, &u.RollbackActivation, true); err != nil {
		return err
	}
	for label, e := range map[string]*updateEffect{"manager": &u.Reopening.Manager, "reconcile": &u.Reopening.Reconcile} {
		if !e.Intent || !e.Done {
			continue
		}
		if err := h.retireUpdateIssuer(ctx, h.effectName(label)); err != nil {
			return err
		}
	}
	return nil
}
func (h *host) retireUpdateActivation(ctx context.Context, a *updateActivation, abort bool) error {
	suffix := "forward"
	if abort {
		suffix = "abort"
	}
	for label, e := range map[string]*updateEffect{"daemon-reload": &a.Reload, "settle-enable": &a.Enable, "observer": &a.Observer, "observer-remove": &a.ObserverRemoval, "user-reload": &a.UserReload} {
		if !e.Intent || !e.Done {
			continue
		}
		unit := "brine-update-" + h.r.Update.ID + "-" + suffix + "-" + label + ".service"
		if err := h.retireUpdateIssuer(ctx, unit); err != nil {
			return err
		}
	}
	return nil
}

func (h *host) removeBootstrapAdmission(ctx context.Context) (err error) {
	u := h.r.Update
	if !u.Aborting || !u.Plan.Bootstrap || u.Reopening == nil || h.updateReconcileRequired() && !u.Reopening.Reconcile.Done {
		return admission.ErrInconsistent
	}
	if err := h.bindAdmission(); err != nil {
		return err
	}
	d, err := h.updateEnv.drain(ctx, h.updatePending(), true)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, d.Close()) }()
	if err := d.SetStage(ctx, admission.Terminal); err != nil {
		return err
	}
	u.RemovalPending = true
	u.RemovedAdmission = [3]bool{}
	if err := h.updateSave(); err != nil {
		return err
	}
	return h.completeBootstrapRemovalHeld(ctx)
}
func (h *host) completeBootstrapRemovalHeld(ctx context.Context) (err error) {
	u := h.r.Update
	if !u.RemovalPending || !u.Aborting || !u.Plan.Bootstrap || h.r.Admission == nil {
		return admission.ErrInconsistent
	}
	fs := h.updateEnv.fs
	if err := h.checkBootstrapRemovalDirectory(); err != nil {
		return err
	}
	for index, name := range []string{"state.json", "stage.lock", "lock"} {
		if err := h.removeBootstrapFile(index, name); err != nil {
			return err
		}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	parent, err := fs.directory("/var/lib", fs.owner)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, parent.Close()) }()
	err = unix.Unlinkat(int(parent.Fd()), "brine-admission", unix.AT_REMOVEDIR)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := parent.Sync(); err != nil {
		return err
	}
	h.r.Admission = nil
	delete(h.r.Dirs, admissionDir)
	delete(h.r.Journal.Intents, "admission")
	h.finishUpdateRetention(u)
	h.r.Update = nil
	return h.updateSave()
}
func (h *host) removeBootstrapFile(index int, name string) error {
	u := h.r.Update
	fs := h.updateEnv.fs
	path := admissionDir + "/" + name
	observed, _, err := fs.read(path)
	if err != nil {
		return err
	}
	if observed.Present {
		if u.RemovedAdmission[index] {
			return admission.ErrInconsistent
		}
		expected := h.r.Admission.Lock
		if name == "stage.lock" {
			expected = h.r.Admission.StageLock
		}
		if name != "state.json" && (observed.Dev != expected.Dev || observed.Ino != expected.Ino) {
			return admission.ErrInconsistent
		}
		if observed.UID != fs.owner || observed.Mode != 0644 {
			return admission.ErrUnavailable
		}
		if err := fs.remove(path); err != nil {
			return err
		}
	}
	u.RemovedAdmission[index] = true
	delete(h.r.Files, path)
	return h.updateSave()
}

func (h *host) recordOpenAdmissionHash() {
	path := admissionDir + "/state.json"
	file := h.r.Files[path]
	raw, _ := admission.Encode(admission.State{Version: 1, Open: true})
	file.Hash = hash(raw)
	h.r.Files[path] = file
}

func (h *host) checkBootstrapRemovalDirectory() error {
	info, err := os.Lstat(h.updateEnv.fs.path(admissionDir))
	if errors.Is(err, os.ErrNotExist) {
		if !h.allBootstrapAdmissionRemoved() {
			return admission.ErrInconsistent
		}
		return nil
	}
	if err != nil || !info.IsDir() {
		return errors.Join(admission.ErrUnavailable, err)
	}
	if updateNodeIdentity(info) != h.r.Admission.Directory {
		return admission.ErrInconsistent
	}
	return nil
}

func (h *host) allBootstrapAdmissionRemoved() bool {
	for _, removed := range h.r.Update.RemovedAdmission {
		if !removed {
			return false
		}
	}
	return true
}

func (h *host) userUpdateUnit(ctx context.Context, unit string) (map[string]string, error) {
	uid := strconv.Itoa(h.r.UID)
	out, err := h.updateCommand(ctx, false, "runuser", "-u", "brine", "--", "/usr/bin/env", "XDG_RUNTIME_DIR=/run/user/"+uid, "DBUS_SESSION_BUS_ADDRESS=unix:path=/run/user/"+uid+"/bus", "/usr/bin/systemctl", "--user", "show", unit, "--property=LoadState,ActiveState,SubState,Result,ExecMainStatus,InvocationID,MainPID,Job")
	if err != nil {
		return nil, err
	}
	p := nativeProperties(out)
	if len(p) != 8 {
		return nil, errors.New("reconcile_readback_incomplete")
	}
	return p, nil
}
func (h *host) handoffUpdateReconcile(ctx context.Context) error {
	if !h.updateReconcileRequired() {
		return nil
	}
	e := &h.r.Update.Reopening.Reconcile
	if e.Observed != "" {
		return nil
	}
	p, err := h.userUpdateUnit(ctx, "brine-reconcile.service")
	if err != nil {
		return err
	}
	if updateReconcileAccepted(p, e.Before) {
		e.Observed = p["InvocationID"] + "/" + p["Job"]
		e.Done = true
		return h.updateSave()
	}
	if p["InvocationID"] != "" && p["InvocationID"] != e.Before {
		return errors.New("reconcile_handoff_pending")
	}
	if err := h.userUpdateEffect(ctx, e, "reconcile", "start", "brine-reconcile.service"); err != nil {
		return err
	}
	p, err = h.userUpdateUnit(ctx, "brine-reconcile.service")
	if err != nil || !updateReconcileAccepted(p, e.Before) {
		return errors.Join(errors.New("reconcile_handoff_pending"), err)
	}
	e.Observed = p["InvocationID"] + "/" + p["Job"]
	return h.updateSave()
}
func updateReconcileAccepted(p map[string]string, before string) bool {
	if p["LoadState"] != "loaded" || p["ActiveState"] == "failed" {
		return false
	}
	if p["Job"] != "" && p["Job"] != "0" {
		return true
	}
	return p["InvocationID"] != "" && p["InvocationID"] != before && p["Result"] == "success"
}

func (h *host) retireUpdateIssuer(ctx context.Context, unit string) error {
	out, err := h.run(ctx, false, "systemctl", "show", unit, "--property=LoadState", "--value")
	if ctx.Err() != nil || out.Truncated {
		return errors.Join(errors.New("issuer_retirement_pending"), err, ctx.Err())
	}
	var exit *localexec.Error
	completed := err == nil && out.ExitCode == 0 || errors.As(err, &exit) && exit.Kind == localexec.Failed && exit.ExitCode == out.ExitCode && out.ExitCode > 0
	if completed && out.Stdout == "not-found\n" {
		return nil
	}
	if err != nil || out.Stdout != "loaded\n" {
		return errors.Join(errors.New("issuer_retirement_pending"), err)
	}
	_, err = h.updateCommand(ctx, true, "systemctl", "stop", unit)
	return err
}

func (h *host) finishUpdateRetention(u *updateRecord) {
	if u.Aborting {
		h.r.LastUpdate = u.PreviousUpdate
		h.r.AbortedUpdate = u.ID
		return
	}
	h.r.LastUpdate = u.ID
	h.r.AbortedUpdate = ""
}

type bootstrapRemovalLeases struct{ files [2]*os.File }

func (l *bootstrapRemovalLeases) Close() error {
	var err error
	for i, f := range l.files {
		if f == nil {
			continue
		}
		err = errors.Join(err, f.Close())
		l.files[i] = nil
	}
	return err
}
func (h *host) completeBootstrapRemoval(ctx context.Context) (err error) {
	leases, err := h.drainBootstrapRemoval(ctx)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, leases.Close()) }()
	return h.completeBootstrapRemovalHeld(ctx)
}
func (h *host) drainBootstrapRemoval(ctx context.Context) (_ *bootstrapRemovalLeases, err error) {
	u := h.r.Update
	if u == nil || !u.RemovalPending || !u.Aborting || !u.Plan.Bootstrap || h.r.Admission == nil {
		return nil, admission.ErrInconsistent
	}
	if err := h.checkBootstrapRemovalDirectory(); err != nil {
		return nil, err
	}
	if err := h.checkBootstrapRemovalState(ctx); err != nil {
		return nil, err
	}
	leases := &bootstrapRemovalLeases{}
	defer func() {
		if err != nil {
			err = errors.Join(err, leases.Close())
		}
	}()
	for index, name := range []string{"lock", "stage.lock"} {
		file, err := h.openBootstrapRemovalLock(name, index)
		if err != nil {
			return nil, err
		}
		leases.files[index] = file
		if err := waitBootstrapRemovalLease(ctx, file); err != nil {
			return nil, err
		}
	}
	return leases, nil
}
func (h *host) openBootstrapRemovalLock(name string, index int) (*os.File, error) {
	fs := h.updateEnv.fs
	if err := fs.protectedUpdateParents(admissionDir + "/" + name); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	file, err := proofOpen(fs.path(admissionDir+"/"+name), unix.O_RDONLY)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var st unix.Stat_t
	if err := unix.Fstat(int(file.Fd()), &st); err != nil {
		return nil, errors.Join(err, file.Close())
	}
	expected := h.r.Admission.Lock
	if index == 1 {
		expected = h.r.Admission.StageLock
	}
	if st.Mode&unix.S_IFMT != unix.S_IFREG || st.Mode&07777 != 0644 || st.Nlink != 1 || int(st.Uid) != fs.owner || expected != (admissionNodeID{uint64(st.Dev), st.Ino}) {
		return nil, errors.Join(admission.ErrInconsistent, file.Close())
	}
	return file, nil
}
func waitBootstrapRemovalLease(ctx context.Context, file *os.File) error {
	if file == nil {
		return nil
	}
	wait, cancel := context.WithTimeout(ctx, admission.DefaultDrainTimeout)
	defer cancel()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		err := unix.Flock(int(file.Fd()), unix.LOCK_EX|unix.LOCK_NB)
		if err == nil {
			return nil
		}
		if !errors.Is(err, unix.EWOULDBLOCK) && !errors.Is(err, unix.EAGAIN) {
			return err
		}
		select {
		case <-wait.Done():
			return errors.Join(admission.ErrDrainTimeout, wait.Err())
		case <-ticker.C:
		}
	}
}

func (h *host) checkBootstrapRemovalState(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	file, raw, err := h.updateEnv.fs.read(admissionDir + "/state.json")
	if err != nil {
		return err
	}
	if !file.Present {
		return nil
	}
	if file.UID != h.updateEnv.fs.owner || file.Mode != 0644 {
		return admission.ErrUnavailable
	}
	state, err := admission.Decode(raw)
	if err != nil {
		return err
	}
	if state.Open || state.Pending.Kind != admission.Update || state.Pending.ID != h.r.Update.ID || state.Pending.Stage != admission.Terminal {
		return admission.ErrInconsistent
	}
	return nil
}

func (h *host) updateReconcileRequired() bool {
	return !h.r.Update.Aborting || !h.r.Update.Plan.addsArtifact(reconcileUnitPath)
}
