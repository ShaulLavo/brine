//go:build linux

package enroll

import (
	"context"
	"errors"
	"io"
	"os"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/ShaulLavo/brine/internal/admission"
	"github.com/ShaulLavo/brine/internal/localexec"
	"golang.org/x/sys/unix"
)

const admissionDir = "/var/lib/brine-admission"
const settleUnitPath = "/etc/systemd/system/brine-admission-settle.service"
const settleUnit = `[Unit]
Description=Settle interrupted Brine admission
Before=systemd-user-sessions.service

[Service]
Type=oneshot
ExecStart=/usr/local/bin/brine host admission-settle
TimeoutStartSec=90s
Restart=no

[Install]
WantedBy=multi-user.target
`

type admissionAbandonment struct {
	Pending admission.Pending `json:"pending"`
}

type admissionNodeID struct{ Dev, Ino uint64 }
type admissionIdentity struct {
	Directory admissionNodeID `json:"directory"`
	Lock      admissionNodeID `json:"lock"`
	StageLock admissionNodeID `json:"stage_lock"`
}

type ownedUpdateDrain struct {
	*admission.Drain
	directory *admission.Directory
}

func (d *ownedUpdateDrain) Close() error { return errors.Join(d.Drain.Close(), d.directory.Close()) }

func (h *host) productionUpdateEnvironment() *updateEnvironment {
	return &updateEnvironment{fs: updateFS{root: "/", owner: 0}, state: func(ctx context.Context) (state admission.State, err error) {
		d, err := admission.OpenDirectory(admissionDir)
		if err != nil {
			return admission.State{}, err
		}
		defer func() { err = errors.Join(err, d.Close()) }()
		return d.Read(ctx)
	}, drain: func(ctx context.Context, p admission.Pending, fresh bool) (updateDrain, error) {
		d, err := admission.OpenDirectory(admissionDir)
		if err != nil {
			return nil, err
		}
		var drain *admission.Drain
		if fresh {
			drain, err = d.CloseAdmission(ctx, p)
		} else {
			drain, err = d.ResumeClosed(ctx, p.Kind, p.ID)
		}
		if err != nil {
			return nil, errors.Join(err, d.Close())
		}
		return &ownedUpdateDrain{Drain: drain, directory: d}, nil
	}, prove: func(ctx context.Context, b AdmissionProofContext, runner bool) error {
		p := AdmissionProofs{Root: "/", ProcRoot: "/proc", CgroupRoot: "/sys/fs/cgroup", MountInfo: "/proc/self/mountinfo", Executor: h.exec}
		if runner {
			_, err := p.ProveRunnerQuiescent(ctx, b)
			return err
		}
		_, err := p.ProveStoreQuiescent(ctx, b)
		return err
	}}
}

func (h *host) admissionFS() updateFS {
	if h.updateEnv != nil {
		return h.updateEnv.fs
	}
	return updateFS{root: "/", owner: 0}
}
func (h *host) admissionSave() error {
	if h.updateEnv != nil {
		return h.updateSave()
	}
	return h.Save(h.r.Journal)
}
func (h *host) readAdmissionIdentity() (identity admissionIdentity, err error) {
	fs := h.admissionFS()
	d, err := fs.directory(admissionDir, fs.owner)
	if err != nil {
		return identity, err
	}
	defer func() { err = errors.Join(err, d.Close()) }()
	var st unix.Stat_t
	if err = unix.Fstat(int(d.Fd()), &st); err != nil {
		return identity, err
	}
	if st.Mode&07777 != 0755 {
		return identity, admission.ErrUnavailable
	}
	identity.Directory = admissionNodeID{uint64(st.Dev), st.Ino}
	for index, name := range []string{"lock", "stage.lock"} {
		observed, _, err := fs.read(admissionDir + "/" + name)
		if err != nil || !observed.Present || observed.UID != fs.owner || observed.Mode != 0644 {
			return identity, errors.Join(admission.ErrUnavailable, err)
		}
		id := admissionNodeID{observed.Dev, observed.Ino}
		if index == 0 {
			identity.Lock = id
		} else {
			identity.StageLock = id
		}
	}
	return identity, nil
}
func (h *host) bindAdmission() error {
	actual, err := h.readAdmissionIdentity()
	if err != nil {
		return errors.Join(admission.ErrUnavailable, err)
	}
	if h.r.Admission == nil || *h.r.Admission != actual {
		return admission.ErrInconsistent
	}
	return nil
}

func (h *host) requireFreshAdmission(ctx context.Context, bootstrap bool) error {
	if h.r.AdmissionAbandonment != nil {
		return h.completeAbandonedAdmission(ctx)
	}
	if h.r.SSHReloadPending {
		return errors.New("SSH reload outcome unknown")
	}
	if h.r.Update != nil {
		return h.refusePendingUpdate(ctx)
	}
	if h.r.Admission == nil && bootstrap {
		if _, err := os.Lstat(h.admissionFS().path(admissionDir)); !errors.Is(err, os.ErrNotExist) {
			return admission.ErrInconsistent
		}
		return nil
	}
	if h.r.Admission == nil && h.r.Journal.Phase != Enrolled && !h.r.Journal.Intents["user"] {
		return nil
	}
	if h.updateEnv == nil {
		h.updateEnv = h.productionUpdateEnvironment()
	}
	if err := h.bindAdmission(); err != nil {
		return err
	}
	state, err := h.updateEnv.state(ctx)
	if err != nil {
		return errors.Join(admission.ErrUnavailable, err)
	}
	if !state.Open {
		if state.Pending.Stage == admission.Admitting {
			return h.abandonAdmission(ctx, *state.Pending)
		}
		return admission.ErrInconsistent
	}
	return h.bindAdmission()
}

func (h *host) abandonAdmission(ctx context.Context, p admission.Pending) error {
	if p.Stage != admission.Admitting || h.r.Update != nil {
		return admission.ErrInconsistent
	}
	h.r.AdmissionAbandonment = &admissionAbandonment{Pending: p}
	if err := h.updateSave(); err != nil {
		return err
	}
	return h.completeAbandonedAdmission(ctx)
}
func (h *host) completeAbandonedAdmission(ctx context.Context) (err error) {
	if h.r.Update != nil {
		return admission.ErrInconsistent
	}
	if h.updateEnv == nil {
		h.updateEnv = h.productionUpdateEnvironment()
	}
	if err := h.bindAdmission(); err != nil {
		return err
	}
	p := h.r.AdmissionAbandonment.Pending
	state, err := h.updateEnv.state(ctx)
	if err != nil {
		return err
	}
	if !state.Open {
		if state.Pending.Kind != p.Kind || state.Pending.ID != p.ID {
			return admission.ErrInconsistent
		}
		return h.reopenAbandonedAdmission(ctx, p)
	}
	h.r.AdmissionAbandonment = nil
	return h.updateSave()
}
func (h *host) reopenAbandonedAdmission(ctx context.Context, p admission.Pending) (err error) {
	d, err := h.updateEnv.drain(ctx, p, false)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, d.Close()) }()
	if err := h.recordAdmissionAbandonment(p); err != nil {
		return err
	}
	if err := d.SetStage(ctx, admission.Terminal); err != nil {
		return err
	}
	if err := d.Reopen(ctx); err != nil {
		return err
	}
	h.r.AdmissionAbandonment = nil
	return h.updateSave()
}

func (h *host) recordAdmissionAbandonment(p admission.Pending) error {
	fs := h.updateEnv.fs
	_, data, err := fs.read(admissionDir + "/history.jsonl")
	if err != nil {
		return err
	}
	line := []byte("{\"abandoned\":\"" + p.ID + "\",\"kind\":\"" + string(p.Kind) + "\"}\n")
	if !strings.Contains(string(data), string(line)) {
		data = append(data, line...)
	}
	count := 0
	for index := len(data) - 2; index >= 0; index-- {
		if data[index] != '\n' {
			continue
		}
		count++
		if count == 63 {
			data = data[index+1:]
			break
		}
	}
	_, err = fs.write(admissionDir+"/history.jsonl", data, 0600, fs.owner, os.Getegid())
	if err != nil {
		return err
	}
	h.r.Files[admissionDir+"/history.jsonl"] = ownedFile{Hash: hash(data), Mode: 0600}
	return h.updateSave()
}

func (h *host) checkAdmissionInstall(ctx context.Context) (bool, error) {
	if h.r.Admission == nil {
		return false, nil
	}
	if err := h.bindAdmission(); err != nil {
		return false, err
	}
	fs := h.admissionFS()
	_, data, err := fs.read(admissionDir + "/state.json")
	if err != nil {
		return false, err
	}
	_, err = admission.Decode(data)
	return err == nil, err
}
func (h *host) installAdmission(ctx context.Context) error {
	if h.r.Admission != nil {
		_, err := h.checkAdmissionInstall(ctx)
		return err
	}
	if h.updateEnv == nil {
		if err := h.dir(admissionDir, 0755, 0, 0); err != nil {
			return err
		}
	} else if err := h.createUpdateAdmissionDirectory(); err != nil {
		return err
	}
	state := admission.State{Version: 1, Open: true}
	if h.r.Update != nil {
		p := h.updatePending()
		p.Stage = admission.Fenced
		state = admission.State{Version: 1, Pending: &p}
	}
	raw, err := admission.Encode(state)
	if err != nil {
		return err
	}
	for _, item := range []struct {
		name string
		data []byte
	}{{"lock", nil}, {"stage.lock", nil}, {"state.json", raw}} {
		if err := h.installAdmissionFile(ctx, item.name, item.data); err != nil {
			return err
		}
	}
	identity, err := h.readAdmissionIdentity()
	if err != nil {
		return err
	}
	if h.r.Journal.Intents == nil {
		h.r.Journal.Intents = map[string]bool{}
	}
	h.r.Journal.Intents["admission"] = true
	h.r.Admission = &identity
	return h.admissionSave()
}
func (h *host) installAdmissionFile(ctx context.Context, name string, data []byte) error {
	path := admissionDir + "/" + name
	if h.updateEnv == nil {
		return h.file(ctx, path, data, 0644, false)
	}
	fs := h.updateEnv.fs
	old, recorded := h.r.Files[path]
	observed, _, err := fs.read(path)
	if err != nil {
		return err
	}
	if !recorded {
		if observed.Present {
			return errors.New("unrecorded admission artifact")
		}
		h.r.Files[path] = ownedFile{Hash: hash(data), Mode: 0644}
		if err := h.admissionSave(); err != nil {
			return err
		}
	} else if old.Hash != hash(data) {
		return admission.ErrInconsistent
	}
	if observed.Present {
		if observed.SHA256 != hash(data) || observed.Mode != 0644 || observed.UID != fs.owner {
			return admission.ErrInconsistent
		}
		return nil
	}
	_, err = fs.write(path, data, 0644, fs.owner, os.Getegid())
	return err
}
func (h *host) undoAdmission(context.Context) error {
	if h.r.Admission != nil {
		if err := h.bindAdmission(); err != nil {
			return err
		}
	}
	for _, name := range []string{"history.jsonl", "state.json", "stage.lock", "lock"} {
		if err := h.restoreFile(admissionDir + "/" + name); err != nil {
			return err
		}
	}
	if err := os.Remove(admissionDir); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	h.r.Admission = nil
	return h.Save(h.r.Journal)
}
func (h *host) checkAdmissionSettle(ctx context.Context) (bool, error) {
	matched, err := h.fileMatches(settleUnitPath, hash([]byte(settleUnit)))
	if err != nil || !matched {
		return matched, err
	}
	r, err := h.run(ctx, false, "systemctl", "is-enabled", "brine-admission-settle.service")
	return err == nil && r.Stdout == "enabled\n", err
}
func (h *host) installAdmissionSettle(ctx context.Context) error {
	if err := h.file(ctx, settleUnitPath, []byte(settleUnit), 0644, false); err != nil {
		return err
	}
	if _, err := h.run(ctx, true, "systemctl", "daemon-reload"); err != nil {
		return err
	}
	_, err := h.run(ctx, true, "systemctl", "enable", "brine-admission-settle.service")
	return err
}
func (h *host) undoAdmissionSettle(ctx context.Context) error {
	if _, err := h.run(ctx, true, "systemctl", "disable", "brine-admission-settle.service"); err != nil {
		return err
	}
	if err := h.restoreFile(settleUnitPath); err != nil {
		return err
	}
	_, err := h.run(ctx, true, "systemctl", "daemon-reload")
	return err
}

// AdmissionSettle never resumes update effects at boot. Terminal reopening is
// the only update tail it may finish; other updates stay durably closed.
func AdmissionSettle(ctx context.Context) (err error) {
	if os.Geteuid() != 0 {
		return errors.New("admission settlement requires root")
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	lock, err := settleRecordLock(ctx)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, lock.Close()) }()
	r, err := readRecord()
	if err != nil || r.ID == "" {
		return errors.Join(admission.ErrInconsistent, err)
	}
	h := &host{r: r, exec: localexec.ExecRunner{}}
	h.updateEnv = h.productionUpdateEnvironment()
	if r.Update == nil {
		return h.requireFreshAdmission(ctx, false)
	}
	if r.Update.Stage != admission.Terminal {
		return errors.New("pending_update")
	}
	return h.resumeUpdateTerminal(ctx)
}
func settleRecordLock(ctx context.Context) (*os.File, error) {
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		lock, err := permissionLock(recordDir + "/lock")
		if err == nil {
			return lock, nil
		}
		select {
		case <-ctx.Done():
			return nil, errors.Join(err, ctx.Err())
		case <-ticker.C:
		}
	}
}

func (h *host) fenceUnit() string { return "user@" + strconv.Itoa(h.r.UID) + ".service" }
func updateNodeIdentity(info os.FileInfo) admissionNodeID {
	st := info.Sys().(*syscall.Stat_t)
	return admissionNodeID{uint64(st.Dev), st.Ino}
}

func (h *host) createUpdateAdmissionDirectory() (err error) {
	fs := h.updateEnv.fs
	d, recorded := h.r.Dirs[admissionDir]
	if !recorded {
		if _, err := os.Lstat(fs.path(admissionDir)); !errors.Is(err, os.ErrNotExist) {
			return admission.ErrInconsistent
		}
		d = ownedDir{Pending: true, Mode: 0755, UID: fs.owner, GID: os.Getegid()}
		h.r.Dirs[admissionDir] = d
		if err := h.updateSave(); err != nil {
			return err
		}
	}
	if err := fs.mkdir(admissionDir, 0755, fs.owner, os.Getegid()); err != nil {
		return err
	}
	dir, err := fs.directory(admissionDir, fs.owner)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, dir.Close()) }()
	info, err := dir.Stat()
	if err != nil {
		return err
	}
	if d.Inode != 0 && d.Inode != updateNodeIdentity(info).Ino {
		return admission.ErrInconsistent
	}
	if d.Pending {
		entries, err := dir.Readdirnames(1)
		if err != nil && !errors.Is(err, io.EOF) {
			return err
		}
		if len(entries) != 0 {
			return admission.ErrInconsistent
		}
	}
	d.Pending = false
	d.Inode = updateNodeIdentity(info).Ino
	h.r.Dirs[admissionDir] = d
	return h.updateSave()
}

func (h *host) refusePendingUpdate(ctx context.Context) error {
	u := h.r.Update
	if u.Plan.Bootstrap && h.r.Admission == nil {
		return errors.New("pending_update")
	}
	if h.updateEnv == nil {
		h.updateEnv = h.productionUpdateEnvironment()
	}
	if err := h.bindAdmission(); err != nil {
		return err
	}
	state, err := h.updateEnv.state(ctx)
	if err != nil {
		return err
	}
	if state.Open {
		if u.Stage != admission.Terminal || u.Reopening == nil {
			return admission.ErrInconsistent
		}
		return errors.New("pending_update")
	}
	if state.Pending.Kind != admission.Update || state.Pending.ID != u.ID {
		return admission.ErrInconsistent
	}
	return errors.New("pending_update")
}

func (h *host) pendingSummary() *EnrollmentPending {
	if u := h.r.Update; u != nil {
		return &EnrollmentPending{Kind: "update", ID: u.ID, Stage: string(u.Stage)}
	}
	if a := h.r.AdmissionAbandonment; a != nil {
		return &EnrollmentPending{Kind: string(a.Pending.Kind), ID: a.Pending.ID, Stage: "admitting"}
	}
	return nil
}
