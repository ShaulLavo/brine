//go:build linux

package enroll

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/ShaulLavo/brine/internal/localexec"
	"golang.org/x/sys/unix"
)

// AdmissionProofContext describes the protected record A2 must bind under L1.
// These caller-supplied values are NOT root-record authority. Before using an
// observation, A2 must independently read and join the enrollment/pending record,
// hold L1 then L2, revoke and drain stage leases at L4, and retain those fences
// through the destructive stage. No observation excludes a later unfenced open.
type AdmissionProofContext struct {
	EnrollmentID    string
	TransactionID   string
	RunnerUID       int
	FenceExpiryDays int64
}

// AdmissionQuiescenceProof is an observation, not a transferable authorization.
// A2 must repeat it immediately before each destructive or schema-changing stage.
type AdmissionQuiescenceProof struct {
	Context     AdmissionProofContext
	Runner      bool
	Processes   int
	Tasks       int
	Descriptors int
}

// AdmissionProofs observes fixed enrollment resources without changing them.
// Roots are injected by trusted composition, never by a remote request. Empty
// roots refuse rather than silently selecting the real host. A2 owns production
// construction and protected-record/lock binding; this A1 API has no callers.
type AdmissionProofs struct {
	Root       string
	ProcRoot   string
	CgroupRoot string
	MountInfo  string
	Executor   localexec.Executor
	limits     admissionProofLimits
}

type admissionProofLimits struct {
	entries int
	tasks   int
	fds     int
	groups  int
	depth   int
	bytes   int
}

const admissionState = "home/brine/.local/state/brine"

var admissionStoreNames = []string{"control.db", "control.db-wal", "control.db-shm"}
var errAdmissionObservation = errors.New("admission quiescence observation refused")

func (p AdmissionProofs) bounded() AdmissionProofs {
	if p.limits == (admissionProofLimits{}) {
		p.limits = admissionProofLimits{entries: 65536, tasks: 65536, fds: 262144, groups: 4096, depth: 32, bytes: 4 << 20}
	}
	return p
}

func (b AdmissionProofContext) validate(runner bool) error {
	if b.RunnerUID <= 0 || int64(b.RunnerUID) > 1<<32-2 || !proofID(b.EnrollmentID) || !proofID(b.TransactionID) {
		return errAdmissionObservation
	}
	if runner && b.FenceExpiryDays != 1 {
		return errAdmissionObservation
	}
	return nil
}

func proofID(id string) bool {
	if len(id) != 32 {
		return false
	}
	for _, c := range id {
		if !strings.ContainsRune("0123456789abcdef", c) {
			return false
		}
	}
	return true
}

func proofPath(path string) bool {
	return filepath.IsAbs(path) && filepath.Clean(path) == path
}

func (p AdmissionProofs) validPaths(runner bool) bool {
	paths := []string{p.Root, p.ProcRoot}
	if runner {
		paths = append(paths, p.CgroupRoot, p.MountInfo)
	}
	for _, path := range paths {
		if !proofPath(path) {
			return false
		}
	}
	return true
}

func proofOpen(path string, flags uint64) (*os.File, error) {
	if !proofPath(path) {
		return nil, errAdmissionObservation
	}
	fd, err := unix.Openat2(unix.AT_FDCWD, path, &unix.OpenHow{
		Flags:   flags | unix.O_CLOEXEC | unix.O_NOFOLLOW | unix.O_NONBLOCK,
		Resolve: unix.RESOLVE_NO_SYMLINKS | unix.RESOLVE_NO_MAGICLINKS,
	})
	if err != nil {
		return nil, errors.Join(errAdmissionObservation, err)
	}
	return os.NewFile(uintptr(fd), path), nil
}

func proofRead(ctx context.Context, path string, limit int) (_ string, err error) {
	if err = ctx.Err(); err != nil {
		return "", err
	}
	f, err := proofOpen(path, unix.O_RDONLY)
	if err != nil {
		return "", err
	}
	defer func() { err = errors.Join(err, f.Close()) }()
	st, err := f.Stat()
	if err != nil || !st.Mode().IsRegular() || st.Size() > int64(limit) {
		return "", errors.Join(errAdmissionObservation, err)
	}
	data, err := io.ReadAll(io.LimitReader(f, int64(limit)+1))
	if err != nil || len(data) > limit || strings.ContainsRune(string(data), 0) {
		return "", errors.Join(errAdmissionObservation, err)
	}
	if err = ctx.Err(); err != nil {
		return "", err
	}
	return string(data), nil
}

func proofNames(ctx context.Context, f *os.File, limit int) ([]string, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}
	entries, err := f.ReadDir(limit + 1)
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, err
	}
	if len(entries) > limit {
		return nil, errAdmissionObservation
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	slices.Sort(names)
	return names, ctx.Err()
}

func proofSameNames(ctx context.Context, f *os.File, limit int, before []string) error {
	after, err := proofNames(ctx, f, limit)
	if err != nil || !slices.Equal(before, after) {
		return errors.Join(errAdmissionObservation, err)
	}
	return nil
}

// The executor retains at most 256 KiB per stream. This observer accepts only
// 16 KiB of stdout and no stderr; larger or truncated observations still refuse.
func (p AdmissionProofs) command(ctx context.Context, path string, args ...string) (localexec.Result, error) {
	if err := ctx.Err(); err != nil {
		return localexec.Result{}, err
	}
	if p.Executor == nil {
		return localexec.Result{}, errAdmissionObservation
	}
	out, err := p.Executor.Execute(ctx, localexec.Command{
		Path: enrollmentToolPath(path), Args: args, Dir: "/", Timeout: 3 * time.Second, OutputLimit: localexec.CommandOutputLimit,
	})
	if ctx.Err() != nil {
		return out, ctx.Err()
	}
	if out.Truncated || len(out.Stdout) > 16384 || len(out.Stderr) != 0 || strings.ContainsRune(out.Stdout, 0) {
		return out, errAdmissionObservation
	}
	return out, err
}

func (p AdmissionProofs) commandText(ctx context.Context, path string, args ...string) (string, error) {
	out, err := p.command(ctx, path, args...)
	if err != nil || out.ExitCode != 0 || !strings.HasSuffix(out.Stdout, "\n") {
		return "", errors.Join(errAdmissionObservation, err)
	}
	return out.Stdout, nil
}

func proofNumber(raw string) (uint64, error) {
	n, err := strconv.ParseUint(raw, 10, 64)
	if err != nil || strconv.FormatUint(n, 10) != raw {
		return 0, errAdmissionObservation
	}
	return n, nil
}

func proofRefuse(part string, err error) error {
	return fmt.Errorf("%s: %w", part, errors.Join(errAdmissionObservation, err))
}
