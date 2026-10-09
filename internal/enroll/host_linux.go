package enroll

import (
	"context"
	"crypto/md5"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/ShaulLavo/brine/internal/caddy"
	"github.com/ShaulLavo/brine/internal/inventory"
	"github.com/ShaulLavo/brine/internal/localexec"
)

const recordDir = "/var/lib/brine-enrollment"
const home = "/home/brine"
const binaryPath = "/usr/local/bin/brine"
const rulePath = "/etc/polkit-1/rules.d/49-brine-caddy.rules"
const mainPath = "/etc/caddy/Caddyfile"
const policy = `polkit.addRule(function(action, subject) {
    if (action.id == "org.freedesktop.systemd1.manage-units" &&
        subject.user == "brine" && action.lookup("unit") == "caddy.service" &&
        action.lookup("verb") == "reload") {
        return polkit.Result.YES;
    }
});
`

type ownedFile struct {
	Before       []byte `json:"before,omitempty"`
	Existed      bool   `json:"existed"`
	Hash         string `json:"hash"`
	Mode         uint32 `json:"mode"`
	OriginalMode uint32 `json:"original_mode"`
	OriginalUID  int    `json:"original_uid"`
	OriginalGID  int    `json:"original_gid"`
}
type ownedDir struct {
	Pending bool   `json:"pending,omitempty"`
	Inode   uint64 `json:"inode"`
	Mode    uint32 `json:"mode"`
	UID     int    `json:"uid"`
	GID     int    `json:"gid"`
}
type hostRecord struct {
	SSHReloadPending bool                   `json:"ssh_reload_pending,omitempty"`
	Runtime          map[string]runtimeFile `json:"runtime"`
	Journal          Journal                `json:"journal"`
	ID               string                 `json:"id"`
	Key              string                 `json:"key"`
	BinaryHash       string                 `json:"binary_hash"`
	GID              int                    `json:"gid"`
	UID              int                    `json:"uid"`
	Files            map[string]ownedFile   `json:"files"`
	Dirs             map[string]ownedDir    `json:"dirs"`
	Packages         []Package              `json:"packages"`
	CaddyActive      bool                   `json:"caddy_active"`
	CaddyEnabled     bool                   `json:"caddy_enabled"`
}
type host struct {
	r           hostRecord
	exec        localexec.Executor
	source      []byte
	identityKey []byte
}

func hash(data []byte) string { sum := sha256.Sum256(data); return hex.EncodeToString(sum[:]) }
func (h *host) Save(j Journal) error {
	h.r.Journal = j
	data, err := json.Marshal(h.r)
	if err != nil {
		return err
	}
	return atomicFile(recordDir+"/record.json", data, 0600)
}
func atomicFile(path string, data []byte, mode os.FileMode) error {
	if err := protectedParents(path); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".brine-write-")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	err = errors.Join(f.Chmod(mode), writeSync(f, data))
	if err != nil {
		return err
	}
	if err = os.Rename(tmp, path); err != nil {
		return err
	}
	dir, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	return errors.Join(dir.Sync(), dir.Close())
}

func protectedParents(path string) error {
	for p := filepath.Dir(path); ; p = filepath.Dir(p) {
		s, err := os.Lstat(p)
		if err != nil {
			return err
		}
		st, ok := s.Sys().(*syscall.Stat_t)
		if !ok || !s.IsDir() || st.Uid != 0 || s.Mode().Perm()&0022 != 0 {
			return errors.New("enrollment requires root-owned, non-writable path parents")
		}
		if p == "/" {
			return nil
		}
	}
}
func boundedRead(path string) ([]byte, error) {
	s, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !s.Mode().IsRegular() || s.Size() > 64<<20 {
		return nil, errors.New("enrollment file type or size refused")
	}
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return nil, errors.New("enrollment opened file type refused")
	}
	data, err := io.ReadAll(io.LimitReader(f, 64<<20+1))
	if len(data) > 64<<20 {
		return nil, errors.New("enrollment file limit")
	}
	return data, err
}
func (h *host) run(ctx context.Context, mutation bool, path string, args ...string) (localexec.Result, error) {
	if slices.Contains([]string{"useradd", "userdel", "runuser"}, path) {
		path = "/usr/sbin/" + path
	}
	return h.exec.Execute(ctx, localexec.Command{Path: path, Args: args, Timeout: 5 * time.Minute, Mutation: mutation, Dir: "/tmp"})
}
func (h *host) owned(ctx context.Context) (bool, error) {
	out, err := h.run(ctx, false, "getent", "passwd", "brine")
	if err != nil {
		var e *localexec.Error
		if errors.As(err, &e) && e.ExitCode == 2 {
			return false, nil
		}
		return false, err
	}
	fields := strings.Split(strings.TrimSpace(out.Stdout), ":")
	if len(fields) != 7 {
		return false, errors.New("invalid runner account")
	}
	return h.r.ID != "" && fields[4] == "brine-enrollment-"+h.r.ID && fields[5] == home && fields[6] == "/bin/sh", nil
}
func (h *host) facts(ctx context.Context, key []byte) (Facts, error) {
	return (Prober{FS: inventory.HostFS{}, Runner: probeRunner{h.exec}, IdentityKey: key, OwnedRunner: h.owned, CheckAuthorization: h.checkAuthorizedKeyPaths}).Collect(ctx)
}
func readRecord() (hostRecord, error) {
	data, err := boundedRead(recordDir + "/record.json")
	if errors.Is(err, os.ErrNotExist) {
		return hostRecord{}, nil
	}
	if err != nil {
		return hostRecord{}, err
	}
	if err = protectedParents(recordDir + "/record.json"); err != nil {
		return hostRecord{}, err
	}
	s, err := os.Lstat(recordDir + "/record.json")
	if err != nil {
		return hostRecord{}, err
	}
	st := s.Sys().(*syscall.Stat_t)
	if st.Uid != 0 || s.Mode().Perm() != 0600 {
		return hostRecord{}, errors.New("enrollment record is not protected")
	}
	var r hostRecord
	d := json.NewDecoder(strings.NewReader(string(data)))
	d.DisallowUnknownFields()
	if err = d.Decode(&r); err != nil {
		return r, err
	}
	if r.ID == "" || r.Files == nil || r.Dirs == nil {
		return r, errors.New("invalid enrollment record")
	}
	return r, nil
}
func HostOperation(ctx context.Context, req HostRequest) (any, error) {
	if os.Geteuid() != 0 {
		return nil, errors.New("enrollment host operations require operator admin access")
	}
	if len(req.IdentityKey) != 32 {
		return nil, errors.New("enrollment identity key must be 32 bytes")
	}
	r, err := readRecord()
	if err != nil {
		return nil, err
	}
	h := &host{r: r, exec: localexec.ExecRunner{}, identityKey: req.IdentityKey}
	if req.Action == "undo-probe" {
		// Undo uses authenticated operator SSH and journal provenance, not deploy
		// environment prerequisites. Unsafe SSH policy must not prevent removal.
		data, err := boundedRead("/etc/ssh/ssh_host_ed25519_key.pub")
		if err != nil {
			return nil, err
		}
		fields := strings.Fields(string(data))
		if len(fields) < 2 {
			return nil, errors.New("host public key unavailable")
		}
		hostKey, err := PublicKey(strings.Join(fields[:2], " "))
		if err != nil {
			return nil, err
		}
		owned, err := h.owned(ctx)
		if err != nil {
			return nil, err
		}
		return Facts{HostKey: hostKey, OwnedRunner: owned}, nil
	}
	if req.Action == "probe" {
		if h.r.ID == "" {
			if err := h.preflight(ctx); err != nil {
				return nil, err
			}
		}
		return h.facts(ctx, req.IdentityKey)
	}
	if req.Action != "apply" && req.Action != "undo" && req.Action != "verify" {
		return nil, errors.New("unknown enrollment action")
	}
	if req.Action == "apply" {
		if h.r.SSHReloadPending {
			return nil, errors.New("SSH reload outcome unknown; inspect and explicitly undo")
		}
		facts, err := h.facts(ctx, req.IdentityKey)
		if err != nil {
			return nil, err
		}
		if err := checkConfirmation(facts, req.Confirmed); err != nil {
			return nil, err
		}
	}
	if err = protectedParents(recordDir); err != nil {
		return nil, err
	}
	if err = os.Mkdir(recordDir, 0700); err != nil && !errors.Is(err, os.ErrExist) {
		return nil, err
	}
	info, err := os.Lstat(recordDir)
	if err != nil || !info.IsDir() || info.Mode().Perm() != 0700 || info.Sys().(*syscall.Stat_t).Uid != 0 {
		return nil, errors.New("unprotected enrollment journal directory")
	}
	lock, err := os.OpenFile(recordDir+"/lock", os.O_CREATE|os.O_RDWR|syscall.O_NOFOLLOW, 0600)
	if err != nil {
		return nil, err
	}
	defer lock.Close()
	if err = syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		return nil, errors.New("enrollment already running")
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
	// Reload under the lock. The deploy user cannot write these records.
	h.r, err = readRecord()
	if err != nil {
		return nil, err
	}
	if req.Action == "undo" {
		if h.r.ID == "" {
			return nil, errors.New("no Brine enrollment record; nothing may be removed")
		}
		if err = h.undoPreflight(); err != nil {
			return nil, err
		}
		if err = Undo(ctx, h, &h.r.Journal, h.steps()); err != nil {
			return nil, err
		}
		if err = h.absence(ctx); err != nil {
			return nil, err
		}
		if err = os.Remove(recordDir + "/record.json"); err != nil {
			return nil, err
		}
		if err = os.Remove(recordDir + "/lock"); err != nil {
			return nil, err
		}
		if err = os.Remove(recordDir); err != nil {
			return nil, err
		}
		return map[string]bool{"removed": true}, nil
	}
	if req.Action == "verify" {
		if h.r.Journal.Phase != Enrolled {
			return nil, errors.New("enrollment incomplete")
		}
		if err = h.finishVerification(ctx); err != nil {
			return nil, err
		}
		return h.facts(ctx, req.IdentityKey)
	}
	key, err := PublicKey(req.DeployKey)
	if err != nil {
		return nil, err
	}
	f, err := h.facts(ctx, req.IdentityKey)
	if err != nil {
		return nil, err
	}
	err = checkConfirmation(f, req.Confirmed)
	if err != nil {
		return nil, err
	}
	path, err := os.Executable()
	if err != nil {
		return nil, err
	}
	h.source, err = boundedRead(path)
	if err != nil {
		return nil, err
	}
	if h.r.ID == "" {
		if err = h.preflight(ctx); err != nil {
			return nil, err
		}

		id := make([]byte, 16)
		if _, err = rand.Read(id); err != nil {
			return nil, err
		}
		active, _ := h.run(ctx, false, "systemctl", "is-active", "caddy.service")
		enabled, _ := h.run(ctx, false, "systemctl", "is-enabled", "caddy.service")
		if strings.TrimSpace(enabled.Stdout) == "masked" {
			return nil, errors.New("preexisting Caddy mask refused")
		}
		h.r = hostRecord{ID: hex.EncodeToString(id), Key: key, BinaryHash: hash(h.source), UID: -1, Files: map[string]ownedFile{}, Dirs: map[string]ownedDir{}, CaddyActive: strings.TrimSpace(active.Stdout) == "active", CaddyEnabled: strings.TrimSpace(enabled.Stdout) == "enabled"}
		h.r.Packages = f.PackageInstall
		if err = h.Save(h.r.Journal); err != nil {
			return nil, err
		}
	} else if h.r.Key != key || (h.r.BinaryHash != hash(h.source) && h.r.Journal.Intents["binary"]) {
		return nil, errors.New("enrollment options differ from durable intent")
	}
	if err := h.checkRecordedTransaction(ctx, f); err != nil {
		return nil, err
	}
	h.r.BinaryHash = hash(h.source)
	if err = Apply(ctx, h, &h.r.Journal, h.steps()); err != nil {
		return nil, err
	}
	return h.facts(ctx, req.IdentityKey)
}

func (h *host) file(ctx context.Context, path string, want []byte, mode os.FileMode, allowExisting bool) error {
	if err := protectedParents(path); err != nil {
		return err
	}
	old, recorded := h.r.Files[path]
	data, err := boundedRead(path)
	existingErr := err
	if !recorded {
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		if err == nil && !allowExisting {
			return errors.New("preexisting file refused")
		}
		old = ownedFile{Before: data, Existed: err == nil, Hash: hash(want), Mode: uint32(mode)}
		if err == nil {
			info, e := os.Lstat(path)
			if e != nil {
				return e
			}
			st := info.Sys().(*syscall.Stat_t)
			old.OriginalMode = uint32(info.Mode().Perm())
			old.OriginalUID = int(st.Uid)
			old.OriginalGID = int(st.Gid)
		}
		h.r.Files[path] = old
		if err = h.Save(h.r.Journal); err != nil {
			return err
		}
	}
	err = existingErr
	if old.Hash != hash(want) || old.Mode != uint32(mode) {
		return errors.New("enrollment file intent changed")
	}
	if err == nil && hash(data) == old.Hash {
		s, err := os.Lstat(path)
		if err != nil {
			return err
		}
		if s.Mode().Perm() != mode || s.Sys().(*syscall.Stat_t).Uid != 0 {
			return errors.New("enrollment file ownership drift")
		}
		return nil
	}
	if err == nil && (!old.Existed || hash(data) != hash(old.Before)) {
		return errors.New("enrollment file content drift")
	}
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return atomicFile(path, want, mode)
}
func (h *host) restoreFile(path string) error {
	old, ok := h.r.Files[path]
	if !ok {
		return nil
	}
	if err := protectedParents(path); err != nil {
		return err
	}
	data, err := boundedRead(path)
	if errors.Is(err, os.ErrNotExist) && !old.Existed {
		return nil
	}
	if err != nil {
		return err
	}
	if old.Existed && hash(data) == hash(old.Before) {
		info, e := os.Lstat(path)
		if e != nil {
			return e
		}
		st := info.Sys().(*syscall.Stat_t)
		if uint32(info.Mode().Perm()) != old.OriginalMode || int(st.Uid) != old.OriginalUID || int(st.Gid) != old.OriginalGID {
			return errors.New("undo refused: restored file metadata changed")
		}
		return nil
	}
	if hash(data) != old.Hash {
		return errors.New("undo refused: recorded file hash changed")
	}
	s, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if s.Mode().Perm() != os.FileMode(old.Mode) || s.Sys().(*syscall.Stat_t).Uid != 0 {
		return errors.New("undo refused: recorded file ownership changed")
	}
	if old.Existed {
		if err := atomicFile(path, old.Before, os.FileMode(old.OriginalMode)); err != nil {
			return err
		}
		return os.Chown(path, old.OriginalUID, old.OriginalGID)
	}
	return os.Remove(path)
}
func (h *host) dir(path string, mode os.FileMode, uid, gid int) error {
	if err := protectedParents(path); err != nil {
		return err
	}
	old, recorded := h.r.Dirs[path]
	if !recorded {
		if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
			return errors.New("preexisting directory refused")
		}
		old = ownedDir{Mode: uint32(mode), UID: uid, GID: gid, Pending: true}
		h.r.Dirs[path] = old
		if err := h.Save(h.r.Journal); err != nil {
			return err
		}
	}
	err := os.Mkdir(path, 0700)
	if err != nil && !errors.Is(err, os.ErrExist) {
		return err
	}
	s, err := os.Lstat(path)
	if err != nil || !s.IsDir() {
		return errors.New("enrollment directory type drift")
	}
	st := s.Sys().(*syscall.Stat_t)
	if old.Inode != 0 && old.Inode != st.Ino {
		return errors.New("enrollment directory identity drift")
	}
	if old.Inode == 0 || old.Pending {
		if _, err := h.checkDirectory(path, old); err != nil {
			return err
		}
		old.Inode = st.Ino
		old.Pending = true
		h.r.Dirs[path] = old
		if err := h.Save(h.r.Journal); err != nil {
			return err
		}
		if err := os.Chown(path, uid, gid); err != nil {
			return err
		}
		if err := os.Chmod(path, mode); err != nil {
			return err
		}
		old.Pending = false
		h.r.Dirs[path] = old
		return h.Save(h.r.Journal)
	}
	if int(st.Uid) != uid || int(st.Gid) != gid || s.Mode().Perm() != mode {
		return errors.New("enrollment directory ownership drift")
	}
	return nil
}
func (h *host) steps() []Step {
	command := func(path string, args ...string) func(context.Context) error {
		return func(ctx context.Context) error { _, err := h.run(ctx, true, path, args...); return err }
	}
	checkUnit := func(verb, want string) func(context.Context) (bool, error) {
		return func(ctx context.Context) (bool, error) {
			r, _ := h.run(ctx, false, "systemctl", verb, "caddy.service")
			return strings.TrimSpace(r.Stdout) == want, nil
		}
	}
	noop := func(context.Context) error { return nil }
	return []Step{
		{Name: "ssh-layout", Check: h.checkSSHLayout, Apply: h.sshLayout, Undo: h.removeSSHLayout},
		{Name: "mask", Check: h.masked, Apply: h.mask, Undo: h.unmask},
		{Name: "packages", Check: h.packagesInstalled, Apply: h.installPackages, Undo: h.removePackages},
		{Name: "user", Check: h.checkUser, Apply: h.createUser, Undo: h.removeUser},
		{Name: "layout", Check: h.checkLayout, Apply: h.layout, Undo: noop},
		{Name: "linger", Check: func(context.Context) (bool, error) {
			_, err := os.Lstat("/var/lib/systemd/linger/brine")
			return err == nil, nil
		}, Apply: command("loginctl", "enable-linger", "brine"), Undo: command("loginctl", "disable-linger", "brine")},
		{Name: "binary", Check: func(context.Context) (bool, error) { return h.fileMatches(binaryPath, h.r.BinaryHash) }, Apply: func(ctx context.Context) error { return h.file(ctx, binaryPath, h.source, 0755, false) }, Undo: func(context.Context) error { return h.restoreFile(binaryPath) }},
		{Name: "polkit", Check: func(context.Context) (bool, error) { return h.fileMatches(rulePath, hash([]byte(policy))) }, Apply: func(ctx context.Context) error { return h.file(ctx, rulePath, []byte(policy), 0644, false) }, Undo: func(context.Context) error { return h.restoreFile(rulePath) }},
		{Name: "caddy-tree", Check: h.checkCaddyTree, Apply: h.caddyTree, Undo: h.removeCaddyTree},
		{Name: "caddy-import", Check: func(context.Context) (bool, error) {
			old, ok := h.r.Files[mainPath]
			if !ok {
				return false, nil
			}
			return h.fileMatches(mainPath, old.Hash)
		}, Apply: h.caddyImport, Undo: func(ctx context.Context) error {
			if err := errors.Join(h.restoreFile(mainPath), h.removeCandidate()); err != nil {
				return err
			}
			if h.r.CaddyActive {
				if _, err := h.run(ctx, false, "caddy", "validate", "--adapter", "caddyfile", "--config", mainPath); err != nil {
					return err
				}
				_, err := h.run(ctx, true, "systemctl", "reload", "caddy.service")
				return err
			}
			return nil
		}},
		{Name: "caddy-validate", Check: func(ctx context.Context) (bool, error) {
			_, err := h.run(ctx, false, "caddy", "validate", "--adapter", "caddyfile", "--config", mainPath)
			if err != nil {
				return false, errors.Join(err, h.restoreFile(mainPath))
			}
			return true, nil
		}, Apply: noop, Undo: noop},
		{Name: "unmask", Check: func(ctx context.Context) (bool, error) {
			r, _ := h.run(ctx, false, "systemctl", "is-enabled", "caddy.service")
			return strings.TrimSpace(r.Stdout) != "masked", nil
		}, Apply: h.unmask, Undo: noop},
		{Name: "caddy-enable", Check: checkUnit("is-enabled", "enabled"), Apply: command("systemctl", "enable", "caddy.service"), Undo: func(ctx context.Context) error {
			if h.r.CaddyEnabled {
				return nil
			}
			return command("systemctl", "disable", "caddy.service")(ctx)
		}},
		{Name: "caddy-start", Check: checkUnit("is-active", "active"), Apply: command("systemctl", "start", "caddy.service"), Undo: func(ctx context.Context) error {
			if h.r.CaddyActive {
				return nil
			}
			return command("systemctl", "stop", "caddy.service")(ctx)
		}},
		{Name: "caddy-writable", Check: h.caddyWritable, Apply: h.allowCaddyWrites, Undo: h.protectCaddyTree},
		{Name: "inventory-key", Check: func(context.Context) (bool, error) {
			return h.fileMatches(sshIdentityPath, hash(h.identityKey))
		}, Apply: func(ctx context.Context) error {
			return h.file(ctx, sshIdentityPath, h.identityKey, 0644, false)
		}, Undo: func(context.Context) error { return h.restoreFile(sshIdentityPath) }},
		{Name: "key", Check: func(context.Context) (bool, error) {
			return h.fileMatches(sshKeyPath, hash(h.keyFile()))
		}, Apply: func(ctx context.Context) error {
			return h.file(ctx, sshKeyPath, h.keyFile(), 0644, false)
		}, Undo: func(context.Context) error { return h.restoreFile(sshKeyPath) }},
		{Name: "ssh-policy", Check: h.checkSSH, Apply: h.installSSH, Undo: h.undoSSH},
		{Name: "bypass", Check: h.bypass, Apply: noop, Undo: func(context.Context) error { return h.removeBypassFiles() }},
	}
}
func (h *host) fileMatches(path, want string) (bool, error) {
	data, err := boundedRead(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	old, ok := h.r.Files[path]
	if ok && old.Existed && hash(data) == hash(old.Before) && hash(data) != want {
		info, e := os.Lstat(path)
		if e != nil {
			return false, e
		}
		st := info.Sys().(*syscall.Stat_t)
		if uint32(info.Mode().Perm()) != old.OriginalMode || int(st.Uid) != old.OriginalUID || int(st.Gid) != old.OriginalGID {
			return false, errors.New("enrollment original file metadata drift")
		}
		return false, nil
	}
	if hash(data) != want {
		return false, errors.New("enrollment file hash drift")
	}

	if !ok {
		return false, errors.New("unrecorded file refused")
	}
	s, err := os.Lstat(path)
	if err != nil {
		return false, err
	}
	if s.Mode().Perm() != os.FileMode(old.Mode) || s.Sys().(*syscall.Stat_t).Uid != 0 {
		return false, errors.New("enrollment file ownership drift")
	}
	return true, nil
}
func (h *host) keyFile() []byte {
	return []byte("restrict,command=\"" + binaryPath + " host serve\" " + h.r.Key + "\n")
}
func (h *host) checkUser(ctx context.Context) (bool, error) {
	owned, err := h.owned(ctx)
	if err != nil {
		return false, err
	}
	if !owned {
		out, err := h.run(ctx, false, "getent", "passwd", "brine")
		if err == nil && out.Stdout != "" {
			return false, errors.New("unowned runner account refused")
		}
		return false, nil
	}
	out, err := h.run(ctx, false, "id", "-u", "brine")
	if err != nil {
		return false, err
	}
	uid, err := strconv.Atoi(strings.TrimSpace(out.Stdout))
	if err != nil {
		return false, err
	}
	if h.r.UID < 0 {
		h.r.UID = uid
		return true, h.Save(h.r.Journal)
	}
	if h.r.UID != uid {
		return false, errors.New("runner identity drift")
	}
	return true, nil
}
func (h *host) createUser(ctx context.Context) error {
	if _, err := os.Lstat(home); !errors.Is(err, os.ErrNotExist) {
		return errors.New("preexisting home refused")
	}
	_, err := h.run(ctx, true, "useradd", "--no-create-home", "--home-dir", home, "--shell", "/bin/sh", "--comment", "brine-enrollment-"+h.r.ID, "brine")
	return err
}
func (h *host) layout(ctx context.Context) error {
	gidResult, err := h.run(ctx, false, "id", "-g", "brine")
	if err != nil {
		return err
	}
	gid, err := strconv.Atoi(strings.TrimSpace(gidResult.Stdout))
	h.r.GID = gid
	if err == nil {
		err = h.Save(h.r.Journal)
	}
	if err != nil {
		return err
	}
	for _, d := range []struct {
		p        string
		mode     os.FileMode
		uid, gid int
	}{{home, 0755, 0, gid}, {home + "/.ssh", 0755, 0, 0}, {home + "/.config", 0700, h.r.UID, gid}, {home + "/.local", 0700, h.r.UID, gid}, {home + "/.cache", 0700, h.r.UID, gid}} {
		if err = h.dir(d.p, d.mode, d.uid, d.gid); err != nil {
			return err
		}
	}
	// Descendants of .local are created without a root write through runner-writable
	// parents. The runner owns this state and creates it with its own authority.
	_, err = h.run(ctx, true, "runuser", "-u", "brine", "--", "/bin/sh", "-c", "umask 077; mkdir -p /home/brine/.local/state/brine")
	return err
}
func (h *host) checkDirectory(p string, d ownedDir) (bool, error) {
	s, err := os.Lstat(p)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil || !s.IsDir() {
		return false, errors.New("directory type drift")
	}
	st := s.Sys().(*syscall.Stat_t)
	if d.Inode == 0 || d.Pending {
		if d.Inode != 0 && st.Ino != d.Inode {
			return false, errors.New("pending directory identity drift")
		}
		if !(st.Uid == 0 && st.Gid == 0) && !(int(st.Uid) == d.UID && int(st.Gid) == d.GID) {
			return false, errors.New("pending directory owner drift")
		}
		if s.Mode().Perm()&0022 != 0 {
			return false, errors.New("pending directory writable by others")
		}
		entries, err := os.ReadDir(p)
		if err != nil {
			return false, err
		}
		if len(entries) != 0 {
			return false, errors.New("pending directory has unowned data")
		}
		return false, nil
	}
	if st.Ino != d.Inode || int(st.Uid) != d.UID || int(st.Gid) != d.GID || uint32(s.Mode().Perm()) != d.Mode {
		return false, errors.New("directory ownership drift")
	}
	return true, nil
}
func (h *host) checkLayout(ctx context.Context) (bool, error) {
	for _, p := range []string{home, home + "/.ssh", home + "/.config", home + "/.local", home + "/.cache"} {
		d, ok := h.r.Dirs[p]
		if !ok {
			return false, nil
		}
		done, err := h.checkDirectory(p, d)
		if err != nil || !done {
			return done, err
		}
	}
	root, err := os.OpenRoot(home)
	if err != nil {
		return false, err
	}
	defer root.Close()
	return runnerStateComplete(root, h.r.UID, h.r.GID)
}
func (h *host) caddyTree(ctx context.Context) error {
	if err := h.dir("/etc/caddy/brine", 0755, 0, 0); err != nil {
		return err
	}
	if err := h.dir("/etc/caddy/brine/gen-0", 0755, 0, 0); err != nil {
		return err
	}
	_, err := os.Lstat("/etc/caddy/brine/current")
	if errors.Is(err, os.ErrNotExist) {
		return os.Symlink("gen-0", "/etc/caddy/brine/current")
	}
	if err != nil {
		return err
	}
	link, err := os.Readlink("/etc/caddy/brine/current")
	if err != nil || link != "gen-0" {
		return errors.New("Caddy current drift")
	}
	return nil
}
func (h *host) checkCaddyTree(context.Context) (bool, error) {
	if _, ok := h.r.Dirs["/etc/caddy/brine/gen-0"]; !ok {
		return false, nil
	}
	for _, p := range []string{"/etc/caddy/brine", "/etc/caddy/brine/gen-0"} {
		d, ok := h.r.Dirs[p]
		if !ok {
			return false, nil
		}
		if d.Inode == 0 || d.Pending {
			return h.checkDirectory(p, d)
		}
		s, err := os.Lstat(p)
		if err != nil || !s.IsDir() {
			return false, errors.New("Caddy directory drift")
		}
		st := s.Sys().(*syscall.Stat_t)
		if st.Uid != 0 || st.Ino != d.Inode {
			return false, errors.New("Caddy directory identity drift")
		}
	}
	link, err := os.Readlink("/etc/caddy/brine/current")
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil || link != "gen-0" {
		return false, errors.New("Caddy current drift")
	}
	return true, nil
}
func (h *host) caddyImport(ctx context.Context) error {
	old, ok := h.r.Files[mainPath]
	var original []byte
	var err error
	if ok {
		original = old.Before
	} else {
		original, err = boundedRead(mainPath)
		if err != nil {
			return err
		}
	}
	input := original
	for _, pkg := range h.r.Packages {
		if strings.Split(pkg.Name, ":")[0] == "caddy" {
			out, e := h.run(ctx, false, "dpkg-query", "-W", "-f=${Conffiles}", "caddy")
			if e != nil {
				return e
			}
			expected := ""
			for _, line := range strings.Split(out.Stdout, "\n") {
				f := strings.Fields(line)
				if len(f) >= 2 && f[0] == mainPath {
					expected = f[1]
				}
			}
			sum := md5.Sum(original)
			if expected == "" || hex.EncodeToString(sum[:]) != expected {
				return errors.New("new Caddy package default config was changed; refusing to replace it")
			}
			input = []byte("# Brine-owned root configuration; no package default site.\n")
		}
	}
	next, err := caddy.EnrollmentRoot(input)
	if err != nil {
		return err
	}
	candidate := recordDir + "/candidate.caddy"
	if err := h.file(ctx, candidate, next, 0644, false); err != nil {
		return err
	}
	err = h.validateAndPromote(ctx, candidate, func() error { return h.file(ctx, mainPath, next, 0644, true) }, func() error { return h.restoreFile(mainPath) })
	return errors.Join(err, h.removeCandidate())
}
func (h *host) removeCaddyTree(context.Context) error {
	if _, ok := h.r.Dirs["/etc/caddy/brine"]; !ok {
		return nil
	}
	link, err := os.Readlink("/etc/caddy/brine/current")
	if err == nil {
		if link != "gen-0" {
			return errors.New("undo refused: Caddy generation changed")
		}
		if err = os.Remove("/etc/caddy/brine/current"); err != nil {
			return err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	for _, p := range []string{"/etc/caddy/brine/gen-0", "/etc/caddy/brine"} {
		d, ok := h.r.Dirs[p]
		if !ok {
			continue
		}
		s, err := os.Lstat(p)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil || !s.IsDir() || s.Sys().(*syscall.Stat_t).Ino != d.Inode {
			return errors.New("undo refused: Caddy directory identity changed")
		}
		if err = os.Remove(p); err != nil {
			return err
		}
	}
	return nil
}
func (h *host) bypass(ctx context.Context) (bool, error) {
	scripts := []string{
		"test ! -w /home && test ! -w /home/brine && test ! -w /home/brine/.ssh && test ! -w /etc/ssh/brine/authorized_keys/brine",
		"! mv /home/brine /home/brine.bypass",
		"! mv /home/brine/.ssh /home/brine/.ssh.bypass",
		"! mv /etc/ssh/brine/authorized_keys/brine /etc/ssh/brine/authorized_keys/brine.bypass",
		"! touch /home/brine/.profile",
		"! touch /home/brine/.bashrc",
		"! touch /home/brine/.bash_logout",
		"! touch /home/brine/.pam_environment",
		"! touch /home/brine/.ssh/environment",
		"! touch /home/brine/.ssh/authorized_keys2",
		"! touch /home/brine/.ssh/authorized_keys",
		"test ! -w /etc/ssh && test ! -w /etc/ssh/sshd_config.d && test ! -w /etc/ssh/brine && test ! -w /etc/ssh/brine/authorized_keys",
	}
	for _, script := range scripts {
		if _, err := h.run(ctx, false, "runuser", "-u", "brine", "--", "/bin/sh", "-c", script); err != nil {
			return false, errors.New("runner SSH bypass check failed")
		}
	}
	entries, err := os.ReadDir(home)
	if err != nil {
		return false, err
	}
	for _, e := range entries {
		if e.Name() == ".bashrc" {
			if _, ok := h.r.Files[home+"/.bashrc"]; ok {
				continue
			}
		}
		if !slices.Contains([]string{".ssh", ".config", ".local", ".cache"}, e.Name()) {
			return false, errors.New("unexpected home startup entry")
		}
	}
	sshEntries, err := os.ReadDir(home + "/.ssh")
	if err != nil {
		return false, err
	}
	for _, e := range sshEntries {
		if _, ok := h.r.Files[home+"/.ssh/"+e.Name()]; !ok {
			return false, errors.New("unexpected home SSH entry")
		}
	}
	for _, p := range plantedSSH {
		data := []byte(h.r.Key + "\n")
		if strings.HasSuffix(p, "/environment") {
			data = []byte("BRINE_BYPASS=planted\n")
		}
		if err = h.file(ctx, p, data, 0644, false); err != nil {
			return false, err
		}
	}
	data := []byte("touch /home/brine/.local/state/brine/startup-ran\n")
	if err = h.file(ctx, home+"/.bashrc", data, 0644, false); err != nil {
		return false, err
	}
	return true, nil
}
func (h *host) finishVerification(ctx context.Context) error {
	if ok, err := h.checkSSH(ctx); err != nil || !ok {
		return errors.Join(err, errors.New("SSH policy incomplete"))
	}
	if err := h.cleanSSHCandidates(); err != nil {
		return err
	}
	if err := h.removeCandidate(); err != nil {
		return err
	}
	if err := h.captureRuntime(ctx); err != nil {
		return err
	}
	if _, err := os.Lstat(home + "/.local/state/brine/startup-ran"); !errors.Is(err, os.ErrNotExist) {
		return errors.New("planted startup file ran")
	}
	if err := h.removeBypassFiles(); err != nil {
		return err
	}
	entries, err := os.ReadDir(home + "/.ssh")
	if err != nil {
		return err
	}
	if len(entries) != 0 {
		return errors.New("home SSH directory not empty after verification")
	}
	delete(h.r.Files, home+"/.bashrc")
	delete(h.r.Journal.Intents, "bypass")
	return h.Save(h.r.Journal)
}
func (h *host) removeUser(ctx context.Context) error {
	owned, err := h.owned(ctx)
	if err != nil {
		return err
	}
	if owned {
		if h.r.UID < 0 {
			if _, err = h.checkUser(ctx); err != nil {
				return err
			}
		}
		_, _ = h.run(ctx, true, "loginctl", "terminate-user", "brine")
		if err = h.waitUserStopped(ctx); err != nil {
			return err
		}
		if err = h.deleteAccount(ctx); err != nil {
			return err
		}

	} else {
		out, e := h.run(ctx, false, "getent", "passwd", "brine")
		if e == nil && out.Stdout != "" {
			return errors.New("unowned runner account during undo")
		}
	}
	if err = h.absence(ctx); err != nil {
		return err
	}
	d, ok := h.r.Dirs[home]
	if !ok {
		return nil
	}
	s, err := os.Lstat(home)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil || !s.IsDir() {
		return errors.New("undo home identity refused")
	}
	st := s.Sys().(*syscall.Stat_t)
	if st.Ino != d.Inode || st.Uid != 0 || s.Mode().Perm() != 0755 {
		return errors.New("undo home ownership refused")
	}
	if err = protectedParents(home); err != nil {
		return err
	}
	if err = h.checkUndoHome(); err != nil {
		return err
	}
	return os.RemoveAll(home)
}
func (h *host) absence(ctx context.Context) error {
	for _, kind := range []string{"passwd", "group"} {
		_, err := h.run(ctx, false, "getent", kind, "brine")
		var e *localexec.Error
		if err == nil {
			return errors.New("runner identity remains")
		}
		if !errors.As(err, &e) || e.ExitCode != 2 {
			return errors.New("runner identity absence is unknown")
		}
	}
	if _, err := os.Lstat("/var/lib/systemd/linger/brine"); !errors.Is(err, os.ErrNotExist) {
		return errors.New("runner linger remains")
	}
	if h.r.UID >= 0 {
		if _, err := os.Lstat("/run/user/" + strconv.Itoa(h.r.UID)); !errors.Is(err, os.ErrNotExist) {
			return errors.New("runner runtime remains")
		}
		r, err := h.run(ctx, false, "pgrep", "-u", strconv.Itoa(h.r.UID))
		var e *localexec.Error
		if err == nil || strings.TrimSpace(r.Stdout) != "" {
			return errors.New("runner processes remain")
		}
		if !errors.As(err, &e) || e.ExitCode != 1 {
			return errors.New("runner process absence is unknown")
		}
	}
	for _, p := range []string{"/etc/subuid", "/etc/subgid"} {
		data, err := boundedRead(p)
		if err != nil {
			return err
		}
		if regexp.MustCompile(`(?m)^(brine|` + strconv.Itoa(h.r.UID) + `):`).Match(data) {
			return errors.New("runner subordinate IDs remain")
		}
	}
	return nil
}

type probeRunner struct{ executor localexec.Executor }

func (p probeRunner) RunStdout(ctx context.Context, path string, args ...string) (string, error) {
	r, err := p.executor.Execute(ctx, localexec.Command{Path: path, Args: args, Timeout: 30 * time.Second, Dir: "/tmp"})
	if r.Truncated {
		return "", errors.New("enrollment probe output truncated")
	}
	return r.Stdout, err
}

func (h *host) preflight(ctx context.Context) error {
	for _, p := range []string{home, binaryPath, "/etc/caddy/brine", rulePath, sshDir, sshPolicyPath} {
		if _, err := os.Lstat(p); !errors.Is(err, os.ErrNotExist) {
			return errors.New("preexisting enrollment resource refused")
		}
	}
	for _, kind := range []string{"passwd", "group"} {
		r, err := h.run(ctx, false, "getent", kind, "brine")
		if err == nil && strings.TrimSpace(r.Stdout) != "" {
			return errors.New("preexisting runner identity refused")
		}
		var e *localexec.Error
		if err != nil && (!errors.As(err, &e) || e.ExitCode != 2) {
			return errors.New("runner identity check unavailable")
		}
	}
	if data, err := boundedRead(mainPath); err == nil {
		s, e := os.Lstat(mainPath)
		if e != nil || s.Sys().(*syscall.Stat_t).Uid != 0 || s.Mode().Perm()&0022 != 0 {
			return errors.New("main Caddyfile must be root-owned and not writable by others")
		}
		if _, err = caddy.EnrollmentRoot(data); err != nil {
			return err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

func (h *host) waitUserStopped(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	timer := time.NewTicker(100 * time.Millisecond)
	defer timer.Stop()
	for {
		r, err := h.run(ctx, false, "pgrep", "-u", strconv.Itoa(h.r.UID))
		var e *localexec.Error
		if errors.As(err, &e) && e.ExitCode == 1 && strings.TrimSpace(r.Stdout) == "" {
			if _, err = os.Lstat("/run/user/" + strconv.Itoa(h.r.UID)); errors.Is(err, os.ErrNotExist) {
				return nil
			}
		}
		if err != nil && (!errors.As(err, &e) || e.ExitCode != 1) {
			return errors.New("runner shutdown outcome unknown")
		}
		select {
		case <-ctx.Done():
			return errors.New("runner processes or runtime directory remain; no account deletion attempted")
		case <-timer.C:
		}
	}
}

func (h *host) deleteAccount(ctx context.Context) error {
	_, deleteErr := h.run(ctx, true, "userdel", "--remove", "brine")
	exists, reconcileErr := h.owned(ctx)
	if reconcileErr != nil || exists {
		return errors.New("account deletion outcome needs reconciliation; no blind retry")
	}
	// Exit 12 is normal for the protected home. Other failures can also follow
	// deletion, so account absence, not the process exit code, decides cleanup.
	if deleteErr != nil {
		var e *localexec.Error
		if !errors.As(deleteErr, &e) {
			return errors.New("account deletion outcome needs reconciliation")
		}
	}
	return nil
}

func (h *host) reconcilePendingDirectories() error {
	for path, d := range h.r.Dirs {
		if d.Inode != 0 && !d.Pending {
			continue
		}
		_, err := os.Lstat(path)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return err
		}
		if err := h.dir(path, os.FileMode(d.Mode), d.UID, d.GID); err != nil {
			return err
		}
	}
	return nil
}

func (h *host) validateAndPromote(ctx context.Context, candidate string, promote func() error, restore func() error) error {
	if _, err := h.run(ctx, false, "caddy", "validate", "--adapter", "caddyfile", "--config", candidate); err != nil {
		return err
	}
	if err := promote(); err != nil {
		return errors.Join(err, restore())
	}
	return nil
}

func (h *host) removeCandidate() error {
	path := recordDir + "/candidate.caddy"
	if _, ok := h.r.Files[path]; !ok {
		return nil
	}
	if err := h.restoreFile(path); err != nil {
		return err
	}
	delete(h.r.Files, path)
	return h.Save(h.r.Journal)
}

func (h *host) checkAuthorizedKeyPaths(context.Context) error {
	for _, path := range []string{"/", "/etc", "/etc/ssh", "/etc/ssh/sshd_config.d", "/etc/ssh/sshd_config", sshPolicyPath, sshDir, sshKeyDir, sshKeyPath, sshIdentityPath, "/home", home, home + "/.ssh", home + "/.ssh/authorized_keys", home + "/.ssh/authorized_keys2"} {
		info, err := os.Lstat(path)
		if errors.Is(err, os.ErrNotExist) && (strings.HasPrefix(path, home) || strings.HasPrefix(path, sshDir) || path == sshPolicyPath) {
			continue
		}
		if err != nil {
			return err
		}
		directory := slices.Contains([]string{"/", "/etc", "/etc/ssh", "/etc/ssh/sshd_config.d", sshDir, sshKeyDir, "/home", home, home + "/.ssh"}, path)
		if err := protectedKeyNode(info, directory); err != nil {
			return err
		}
	}
	return nil
}
func protectedKeyNode(info os.FileInfo, directory bool) error {
	if info.IsDir() != directory || (!directory && !info.Mode().IsRegular()) || info.Sys().(*syscall.Stat_t).Uid != 0 || info.Mode().Perm()&0022 != 0 {
		return errors.New("SSH key location or parent is not root-owned and protected")
	}
	return nil
}

func runnerStateComplete(root *os.Root, uid, gid int) (bool, error) {
	for _, path := range []string{".local/state", ".local/state/brine"} {
		info, err := root.Lstat(path)
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		if err != nil {
			return false, err
		}
		st := info.Sys().(*syscall.Stat_t)
		if !info.IsDir() || int(st.Uid) != uid || int(st.Gid) != gid || info.Mode().Perm() != 0700 {
			return false, errors.New("runner state directory drift")
		}
	}
	return true, nil
}
