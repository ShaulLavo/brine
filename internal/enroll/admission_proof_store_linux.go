//go:build linux

package enroll

import (
	"context"
	"crypto/sha256"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"time"

	"golang.org/x/sys/unix"
)

type admissionFileIdentity struct{ dev, ino uint64 }
type admissionStoreView struct {
	directory admissionFileIdentity
	files     map[string]admissionFileIdentity
}
type admissionFDView struct {
	identity   admissionFileIdentity
	linkSHA256 [32]byte
}
type admissionProcessView struct {
	start string
	uids  string
	fds   map[string]admissionFDView
}

// ProveStoreQuiescent checks all visible processes, not only the recorded UID.
// Missing sidecars are allowed; a missing state directory is not. Two identical
// bounded scans detect observed churn, but cannot replace A2's durable fence.
func (p AdmissionProofs) ProveStoreQuiescent(ctx context.Context, binding AdmissionProofContext) (AdmissionQuiescenceProof, error) {
	p = p.bounded()
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	return p.proveStoreQuiescent(ctx, binding)
}

func (p AdmissionProofs) proveStoreQuiescent(ctx context.Context, binding AdmissionProofContext) (AdmissionQuiescenceProof, error) {
	if err := binding.validate(false); err != nil {
		return AdmissionQuiescenceProof{}, err
	}
	if !p.validPaths(false) {
		return AdmissionQuiescenceProof{}, errAdmissionObservation
	}
	before, err := p.storeView(binding.RunnerUID)
	if err != nil {
		return AdmissionQuiescenceProof{}, proofRefuse("store names", err)
	}
	first, count, err := p.processView(ctx, before, -1)
	if err != nil {
		return AdmissionQuiescenceProof{}, proofRefuse("store holders", err)
	}
	second, _, err := p.processView(ctx, before, -1)
	if err != nil || !reflect.DeepEqual(first, second) {
		return AdmissionQuiescenceProof{}, proofRefuse("process view changed", err)
	}
	after, err := p.storeView(binding.RunnerUID)
	if err != nil || !reflect.DeepEqual(before, after) {
		return AdmissionQuiescenceProof{}, proofRefuse("store names changed", err)
	}
	if err := ctx.Err(); err != nil {
		return AdmissionQuiescenceProof{}, err
	}
	return AdmissionQuiescenceProof{Context: binding, Processes: len(first), Descriptors: count}, nil
}

func fileIdentity(st unix.Stat_t) admissionFileIdentity {
	return admissionFileIdentity{dev: uint64(st.Dev), ino: st.Ino}
}

func (p AdmissionProofs) storeView(uid int) (_ admissionStoreView, err error) {
	dir, err := proofOpen(filepath.Join(p.Root, admissionState), unix.O_RDONLY|unix.O_DIRECTORY)
	if err != nil {
		return admissionStoreView{}, err
	}
	defer func() { err = errors.Join(err, dir.Close()) }()
	var st unix.Stat_t
	if err = unix.Fstat(int(dir.Fd()), &st); err != nil || int(st.Uid) != uid || st.Mode&0077 != 0 {
		return admissionStoreView{}, errors.Join(errAdmissionObservation, err)
	}
	view := admissionStoreView{directory: fileIdentity(st), files: make(map[string]admissionFileIdentity)}
	for _, name := range admissionStoreNames {
		if err := storeNameView(dir, name, uid, view.files); err != nil {
			return admissionStoreView{}, err
		}
	}
	return view, nil
}

func storeNameView(dir *os.File, name string, uid int, files map[string]admissionFileIdentity) error {
	var st unix.Stat_t
	err := unix.Fstatat(int(dir.Fd()), name, &st, unix.AT_SYMLINK_NOFOLLOW)
	if errors.Is(err, unix.ENOENT) {
		return nil
	}
	if err != nil || st.Mode&unix.S_IFMT != unix.S_IFREG || int(st.Uid) != uid || st.Nlink != 1 || st.Mode&0077 != 0 {
		return errors.Join(errAdmissionObservation, err)
	}
	files[name] = fileIdentity(st)
	return nil
}

func (p AdmissionProofs) processView(ctx context.Context, store admissionStoreView, refuseUID int) (_ map[string]admissionProcessView, count int, err error) {
	proc, err := proofOpen(p.ProcRoot, unix.O_RDONLY|unix.O_DIRECTORY)
	if err != nil {
		return nil, 0, err
	}
	defer func() { err = errors.Join(err, proc.Close()) }()
	names, err := proofNames(ctx, proc, p.limits.entries)
	if err != nil {
		return nil, 0, err
	}
	view := make(map[string]admissionProcessView)
	for _, pid := range names {
		if pid[0] < '0' || pid[0] > '9' {
			continue
		}
		if n, err := proofNumber(pid); err != nil || n == 0 || n > 1<<31-1 {
			return nil, count, errAdmissionObservation
		}
		process, n, err := p.scanProcess(ctx, pid, store, refuseUID, p.limits.fds-count)
		if err != nil {
			return nil, count, err
		}
		view[pid] = process
		count += n
	}
	return view, count, proofSameNames(ctx, proc, p.limits.entries, names)
}

func (p AdmissionProofs) scanProcess(ctx context.Context, pid string, store admissionStoreView, uid, remaining int) (_ admissionProcessView, count int, err error) {
	base := filepath.Join(p.ProcRoot, pid)
	start, uids, err := p.processFacts(ctx, base, pid, uid)
	if err != nil {
		return admissionProcessView{}, 0, err
	}
	fd, err := proofOpen(filepath.Join(base, "fd"), unix.O_RDONLY|unix.O_DIRECTORY)
	if err != nil {
		return admissionProcessView{}, 0, err
	}
	defer func() { err = errors.Join(err, fd.Close()) }()
	names, err := proofNames(ctx, fd, remaining)
	if err != nil {
		return admissionProcessView{}, 0, err
	}
	fds := make(map[string]admissionFDView, len(names))
	for _, name := range names {
		if err := ctx.Err(); err != nil {
			return admissionProcessView{}, 0, err
		}
		observation, err := p.fdView(fd, name, store)
		if err != nil {
			return admissionProcessView{}, 0, err
		}
		fds[name] = observation
	}
	if err = proofSameNames(ctx, fd, remaining, names); err != nil {
		return admissionProcessView{}, 0, err
	}
	end, endUIDs, err := p.processFacts(ctx, base, pid, uid)
	if err != nil || start != end || uids != endUIDs {
		return admissionProcessView{}, 0, errors.Join(errAdmissionObservation, err)
	}
	return admissionProcessView{start: start, uids: uids, fds: fds}, len(names), nil
}

func (p AdmissionProofs) processFacts(ctx context.Context, base, pid string, refuseUID int) (string, string, error) {
	stat, err := proofRead(ctx, filepath.Join(base, "stat"), 16384)
	if err != nil {
		return "", "", err
	}
	start, err := proofProcessStat(stat, pid)
	if err != nil {
		return "", "", err
	}
	status, err := proofRead(ctx, filepath.Join(base, "status"), 65536)
	if err != nil || !strings.HasSuffix(status, "\n") {
		return "", "", errors.Join(errAdmissionObservation, err)
	}
	uids, err := proofUIDs(status, refuseUID)
	return start, uids, err
}

func proofProcessStat(stat, pid string) (string, error) {
	end := strings.LastIndex(stat, ") ")
	if !strings.HasPrefix(stat, pid+" (") || end < 0 || !strings.HasSuffix(stat, "\n") {
		return "", errAdmissionObservation
	}
	fields := strings.Fields(stat[end+2:])
	if len(fields) != 50 || len(fields[0]) != 1 || !strings.ContainsAny(fields[0], "RSDZTtXxKWPIN") {
		return "", errAdmissionObservation
	}
	for _, field := range fields[1:] {
		if !proofStatNumber(field) {
			return "", errAdmissionObservation
		}
	}
	if _, err := proofNumber(fields[19]); err != nil {
		return "", err
	}
	return fields[19], nil
}

func proofStatNumber(raw string) bool {
	if !strings.HasPrefix(raw, "-") {
		_, err := proofNumber(raw)
		return err == nil
	}
	value, err := strconv.ParseInt(raw, 10, 64)
	return err == nil && value < 0 && strconv.FormatInt(value, 10) == raw
}

func proofUIDs(status string, refuseUID int) (string, error) {
	var found string
	for _, line := range strings.Split(status, "\n") {
		if !strings.HasPrefix(line, "Uid:") {
			continue
		}
		if found != "" || len(strings.Fields(line)) != 5 {
			return "", errAdmissionObservation
		}
		found = strings.TrimPrefix(line, "Uid:")
	}
	if found == "" {
		return "", errAdmissionObservation
	}
	for _, raw := range strings.Fields(found) {
		n, err := proofNumber(raw)
		if err != nil || n > 1<<32-2 || strconv.FormatInt(int64(refuseUID), 10) == raw {
			return "", errors.Join(errAdmissionObservation, err)
		}
	}
	return found, nil
}

func (p AdmissionProofs) fdView(dir *os.File, name string, store admissionStoreView) (admissionFDView, error) {
	if _, err := proofNumber(name); err != nil {
		return admissionFDView{}, err
	}
	var st unix.Stat_t
	if err := unix.Fstatat(int(dir.Fd()), name, &st, unix.AT_SYMLINK_NOFOLLOW); err != nil || st.Mode&unix.S_IFMT != unix.S_IFLNK {
		return admissionFDView{}, errors.Join(errAdmissionObservation, err)
	}
	link := make([]byte, 4097)
	n, err := unix.Readlinkat(int(dir.Fd()), name, link)
	if err != nil || n == 0 || n == len(link) {
		return admissionFDView{}, errors.Join(errAdmissionObservation, err)
	}
	target := string(link[:n])
	if err := unix.Fstatat(int(dir.Fd()), name, &st, 0); err != nil {
		return admissionFDView{}, err
	}
	identity := fileIdentity(st)
	for _, recorded := range store.files {
		if identity == recorded {
			return admissionFDView{}, errAdmissionObservation
		}
	}
	if p.deletedStoreName(target) {
		return admissionFDView{}, errAdmissionObservation
	}
	return admissionFDView{identity: identity, linkSHA256: sha256.Sum256(link[:n])}, nil
}

func (p AdmissionProofs) deletedStoreName(target string) bool {
	if !strings.HasSuffix(target, " (deleted)") {
		return false
	}
	path := strings.TrimSuffix(target, " (deleted)")
	for _, name := range admissionStoreNames {
		if path == filepath.Join(p.Root, admissionState, name) || path == "/"+admissionState+"/"+name {
			return true
		}
	}
	return false
}
