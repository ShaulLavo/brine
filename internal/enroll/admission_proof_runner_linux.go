//go:build linux

package enroll

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"time"

	"github.com/ShaulLavo/brine/internal/localexec"
	"golang.org/x/sys/unix"
)

type admissionRunnerView struct {
	passwd string
	// The shadow password field is never retained or returned.
	expiry  string
	manager string
	pam     string
	mounts  string
	cgroups map[string]admissionFileIdentity
}

// ProveRunnerQuiescent observes an already-issued runner fence. It never expires
// an account, disables linger, stops a service, or acquires admission authority.
// A2 must bind FenceExpiryDays to its durable intent and preserve the fence.
func (p AdmissionProofs) ProveRunnerQuiescent(ctx context.Context, binding AdmissionProofContext) (AdmissionQuiescenceProof, error) {
	p = p.bounded()
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	if err := binding.validate(true); err != nil {
		return AdmissionQuiescenceProof{}, err
	}
	if !p.validPaths(true) {
		return AdmissionQuiescenceProof{}, errAdmissionObservation
	}
	first, err := p.runnerView(ctx, binding)
	if err != nil {
		return AdmissionQuiescenceProof{}, proofRefuse("runner fence", err)
	}
	store, err := p.storeView(binding.RunnerUID)
	if err != nil {
		return AdmissionQuiescenceProof{}, err
	}
	processes, _, err := p.processView(ctx, store, binding.RunnerUID)
	if err != nil {
		return AdmissionQuiescenceProof{}, proofRefuse("runner processes", err)
	}
	proof, err := p.ProveStoreQuiescent(ctx, binding)
	if err != nil {
		return AdmissionQuiescenceProof{}, err
	}
	second, err := p.runnerView(ctx, binding)
	if err != nil || !reflect.DeepEqual(first, second) {
		return AdmissionQuiescenceProof{}, proofRefuse("runner fence changed", err)
	}
	after, _, err := p.processView(ctx, store, binding.RunnerUID)
	if err != nil || !reflect.DeepEqual(processes, after) {
		return AdmissionQuiescenceProof{}, proofRefuse("runner processes changed", err)
	}
	if err := ctx.Err(); err != nil {
		return AdmissionQuiescenceProof{}, err
	}
	finalStore, err := p.storeView(binding.RunnerUID)
	if err != nil || !reflect.DeepEqual(store, finalStore) {
		return AdmissionQuiescenceProof{}, proofRefuse("runner store changed", err)
	}
	proof.Runner = true
	return proof, nil
}

func (p AdmissionProofs) runnerView(ctx context.Context, b AdmissionProofContext) (admissionRunnerView, error) {
	passwd, err := p.commandText(ctx, "getent", "passwd", "brine")
	if err != nil || !proofPasswd(passwd, b) {
		return admissionRunnerView{}, errors.Join(errAdmissionObservation, err)
	}
	shadow, err := p.commandText(ctx, "getent", "shadow", "brine")
	if err != nil || !proofExpiry(shadow, b.FenceExpiryDays) {
		return admissionRunnerView{}, errors.Join(errAdmissionObservation, err)
	}
	if err := p.lingerAbsent(); err != nil {
		return admissionRunnerView{}, err
	}
	unit := "user@" + strconv.Itoa(b.RunnerUID) + ".service"
	manager, err := p.commandText(ctx, "systemctl", "show", unit, "--property=LoadState,ActiveState,SubState")
	if err != nil || !proofInactiveManager(manager) {
		return admissionRunnerView{}, errors.Join(errAdmissionObservation, err)
	}
	if err := p.noRunnerProcesses(ctx, b.RunnerUID); err != nil {
		return admissionRunnerView{}, err
	}
	pam, err := proofRead(ctx, filepath.Join(p.Root, "etc/pam.d/runuser"), 16384)
	if err != nil || !proofRunuserPAM(pam) {
		return admissionRunnerView{}, errors.Join(errAdmissionObservation, err)
	}
	if err := p.sshPAM(ctx); err != nil {
		return admissionRunnerView{}, err
	}
	mounts, err := p.readMountInfo(ctx)
	if err != nil || !proofMounts(mounts) {
		return admissionRunnerView{}, errors.Join(errAdmissionObservation, err)
	}
	groups := make(map[string]admissionFileIdentity)
	group := filepath.Join(p.CgroupRoot, "user.slice", "user-"+strconv.Itoa(b.RunnerUID)+".slice")
	entries := 0
	if err := p.emptyCgroups(ctx, group, 0, &entries, groups); err != nil {
		return admissionRunnerView{}, err
	}
	return admissionRunnerView{passwd: passwd, expiry: strconv.FormatInt(b.FenceExpiryDays, 10), manager: manager, pam: pam, mounts: mounts, cgroups: groups}, nil
}

func (p AdmissionProofs) readMountInfo(ctx context.Context) (string, error) {
	path := p.MountInfo
	// /proc/self is a kernel magic link. Select our numeric PID instead of
	// weakening the no-symlink rule for caller-selected fixture paths.
	if p.ProcRoot == "/proc" && path == "/proc/self/mountinfo" {
		path = filepath.Join(p.ProcRoot, strconv.Itoa(os.Getpid()), "mountinfo")
	}
	return proofRead(ctx, path, p.limits.bytes)
}

func proofPasswd(raw string, b AdmissionProofContext) bool {
	fields := strings.Split(strings.TrimSuffix(raw, "\n"), ":")
	return len(fields) == 7 && fields[0] == "brine" && fields[2] == strconv.Itoa(b.RunnerUID) && fields[4] == "brine-enrollment-"+b.EnrollmentID && fields[5] == home && fields[6] == "/bin/sh"
}

func proofExpiry(raw string, expiry int64) bool {
	fields := strings.Split(strings.TrimSuffix(raw, "\n"), ":")
	return len(fields) == 9 && fields[0] == "brine" && fields[7] == strconv.FormatInt(expiry, 10) && !strings.Contains(strings.TrimSuffix(raw, "\n"), "\n")
}

func (p AdmissionProofs) lingerAbsent() (err error) {
	dir, err := proofOpen(filepath.Join(p.Root, "var/lib/systemd/linger"), unix.O_RDONLY|unix.O_DIRECTORY)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, dir.Close()) }()
	var st unix.Stat_t
	err = unix.Fstatat(int(dir.Fd()), "brine", &st, unix.AT_SYMLINK_NOFOLLOW)
	if errors.Is(err, unix.ENOENT) {
		return nil
	}
	return errors.Join(errAdmissionObservation, err)
}

func proofInactiveManager(raw string) bool {
	lines := strings.Split(strings.TrimSuffix(raw, "\n"), "\n")
	want := map[string]string{"LoadState": "loaded", "ActiveState": "inactive", "SubState": "dead"}
	if len(lines) != len(want) {
		return false
	}
	for _, line := range lines {
		key, value, ok := strings.Cut(line, "=")
		if !ok || want[key] != value || value == "" {
			return false
		}
		delete(want, key)
	}
	return len(want) == 0
}

func (p AdmissionProofs) noRunnerProcesses(ctx context.Context, uid int) error {
	out, err := p.command(ctx, "pgrep", "-u", strconv.Itoa(uid))
	var exit *localexec.Error
	if err != nil && (!errors.As(err, &exit) || exit.Kind != localexec.Failed || exit.ExitCode != 1) {
		return err
	}
	if out.ExitCode != 1 || out.Stdout != "" {
		return errAdmissionObservation
	}
	return nil
}

func (p AdmissionProofs) sshPAM(ctx context.Context) error {
	for _, connection := range sshConnections {
		out, err := p.commandText(ctx, "/usr/sbin/sshd", "-T", "-C", connection)
		if err != nil {
			return err
		}
		if err := checkSSHValues(out, map[string]string{"usepam": "yes"}); err != nil {
			return err
		}
	}
	return nil
}

func proofRunuserPAM(raw string) bool {
	if !strings.HasSuffix(raw, "\n") || strings.Contains(raw, "\\") {
		return false
	}
	count := 0
	for _, line := range strings.Split(raw, "\n") {
		fields := strings.Fields(strings.SplitN(line, "#", 2)[0])
		if len(fields) == 0 {
			continue
		}
		if len(fields) < 3 || !proofPAMType(fields[0]) || !proofPAMControl(fields[1]) {
			return false
		}
		count++
	}
	return count > 0
}

func proofPAMControl(raw string) bool {
	switch raw {
	case "required", "requisite", "sufficient", "optional", "include", "substack":
		return true
	default:
		return false
	}
}

func proofPAMType(raw string) bool {
	kind := strings.TrimPrefix(raw, "-")
	return kind == "auth" || kind == "session" || kind == "password"
}

func (p AdmissionProofs) emptyCgroups(ctx context.Context, path string, depth int, entries *int, groups map[string]admissionFileIdentity) (err error) {
	if depth > p.limits.depth || len(groups) >= p.limits.groups {
		return errAdmissionObservation
	}
	dir, err := proofOpen(path, unix.O_RDONLY|unix.O_DIRECTORY)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, dir.Close()) }()
	var st unix.Stat_t
	if err := unix.Fstat(int(dir.Fd()), &st); err != nil {
		return err
	}
	groups[path] = fileIdentity(st)
	procs, err := proofRead(ctx, filepath.Join(path, "cgroup.procs"), 65536)
	if err != nil || procs != "" {
		return errors.Join(errAdmissionObservation, err)
	}
	names, err := proofNames(ctx, dir, p.limits.entries-*entries)
	if err != nil {
		return err
	}
	*entries += len(names)
	for _, name := range names {
		if err := p.cgroupEntry(ctx, dir, name, depth, entries, groups); err != nil {
			return err
		}
	}
	return proofSameNames(ctx, dir, len(names), names)
}

func (p AdmissionProofs) cgroupEntry(ctx context.Context, dir *os.File, name string, depth int, entries *int, groups map[string]admissionFileIdentity) error {
	var st unix.Stat_t
	if err := unix.Fstatat(int(dir.Fd()), name, &st, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return err
	}
	if st.Mode&unix.S_IFMT == unix.S_IFLNK {
		return errAdmissionObservation
	}
	if st.Mode&unix.S_IFMT != unix.S_IFDIR {
		return nil
	}
	return p.emptyCgroups(ctx, filepath.Join(dir.Name(), name), depth+1, entries, groups)
}
