//go:build linux

package quadlet

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"syscall"

	"github.com/ShaulLavo/brine/internal/target"
)

const (
	ActiveDirectory   = ".config/containers/systemd"
	stagingDirectory  = ".local/share/brine/quadlet/staging"
	rollbackDirectory = ".local/share/brine/quadlet/rollback"
	maxUnitBytes      = 4 << 20
)

// Candidate isolates the rendered unit. The adapter runs the generator with
// --user --dryrun and QUADLET_UNIT_DIRS=Directory, verifies the expected service,
// and bounds time/output. Literal environment settings must not be logged.
type Candidate struct {
	Directory string
	UnitName  string
}
type Validator interface {
	Validate(context.Context, Candidate) error
}

var (
	ErrPublicationUnknown = errors.New("quadlet: publication requires reconciliation")
	ErrUnowned            = errors.New("quadlet: artifact is not owned by Brine")
	ErrDrift              = errors.New("quadlet: recorded artifact has drifted")
)

type OwnershipReason string

const (
	Unrecorded      OwnershipReason = "unrecorded"
	MissingRecorded OwnershipReason = "missing_recorded"
	HashMismatch    OwnershipReason = "hash_mismatch"
	UnsafeFile      OwnershipReason = "unsafe_file"
	UnsafeParent    OwnershipReason = "unsafe_parent"
)

// OwnershipError contains no file contents or untrusted input.
type OwnershipError struct{ Reason OwnershipReason }

func (e *OwnershipError) Error() string {
	return "quadlet: ownership refusal (" + string(e.Reason) + ")"
}
func (e *OwnershipError) Unwrap() error {
	if e.Reason == HashMismatch || e.Reason == MissingRecorded {
		return ErrDrift
	}
	return ErrUnowned
}
func refuseOwnership(reason OwnershipReason) error { return &OwnershipError{Reason: reason} }

// Each state names the next step, not an effect assumed to have succeeded.
// Sync states remain pending when fsync fails, including after rename/removal.
type PublicationState string

const (
	PathsPending        PublicationState = "paths_pending"
	PredecessorPending  PublicationState = "predecessor_pending"
	CandidatePending    PublicationState = "candidate_pending"
	RenamePending       PublicationState = "rename_pending"
	ActiveSyncPending   PublicationState = "active_sync_pending"
	SourceSyncPending   PublicationState = "source_sync_pending"
	CleanupPending      PublicationState = "cleanup_pending"
	StagingSyncPending  PublicationState = "staging_sync_pending"
	PublicationComplete PublicationState = "complete"
)

type Publication struct {
	State     PublicationState `json:"state"`
	Directory string           `json:"directory,omitempty"`
}

// Receipt is trusted operation/control state, never input from an app or client.
// PrepareActivation is effect-free. Production callers persist its result before
// AdvanceActivation, and persist each returned state before advancing again.
// Rollback uses the recorded old/new hashes, never an in-file ownership marker.
type Receipt struct {
	UnitName      string      `json:"unit_name"`
	InstalledHash string      `json:"installed_hash"`
	PreviousHash  string      `json:"previous_hash,omitempty"`
	Home          string      `json:"home"`
	Activation    Publication `json:"activation"`
	Rollback      Publication `json:"rollback"`
}

func (r Receipt) PreviousPath() string {
	if r.PreviousHash == "" {
		return ""
	}
	return filepath.Join(rollbackDirectory, r.UnitName+"-"+strings.TrimPrefix(r.PreviousHash, "sha256:"))
}

// Manager requires D1's host mutation lock across processes and the D7 runner
// directory layout. Activate/Resume are convenience loops; durable apply uses
// Prepare/Advance with its own journal. This package never starts services.
type Manager struct {
	mu        sync.Mutex
	root      *os.Root
	validator Validator
	syncDir   func(string) error
	syncFile  func(*os.File) error
}

func NewManager(home string, v Validator) (*Manager, error) { return newManager(home, v, nil) }
func newManager(home string, v Validator, dirSync func(*os.Root, string) error) (*Manager, error) {
	if v == nil {
		return nil, fmt.Errorf("quadlet: validation hook required")
	}
	home, err := filepath.Abs(home)
	if err != nil {
		return nil, err
	}
	root, err := os.OpenRoot(home)
	if err != nil {
		return nil, err
	}
	m := &Manager{root: root, validator: v, syncFile: func(f *os.File) error { return f.Sync() }}
	m.syncDir = m.syncDirectory
	if dirSync != nil {
		m.syncDir = func(path string) error { return dirSync(root, path) }
	}
	if err = m.checkDirectories(); err != nil {
		root.Close()
		return nil, err
	}
	return m, nil
}
func (m *Manager) Close() error { m.mu.Lock(); defer m.mu.Unlock(); return m.root.Close() }

var unitNamePattern = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?\.container$`)
var stageTokenPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{20,128}$`)

// Owned units must come from authoritative BrineState, not host inventory or
// comments in unit files. A missing entry grants creation only at an absent path.
func (m *Manager) PrepareActivation(u Unit, owned []target.Unit) (Receipt, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !unitNamePattern.MatchString(u.name) || u.content == "" || len(u.content) > maxUnitBytes {
		return Receipt{}, fmt.Errorf("quadlet: rendered unit required")
	}
	previous := ""
	seen := map[string]bool{}
	for _, record := range owned {
		if record.Name == "" || filepath.Base(record.Name) != record.Name || !hashPattern.MatchString(record.Hash) || seen[record.Name] {
			return Receipt{}, fmt.Errorf("quadlet: invalid committed unit records")
		}
		seen[record.Name] = true
		if record.Name == u.name {
			previous = record.Hash
		}
	}
	return Receipt{UnitName: u.name, InstalledHash: u.Hash(), PreviousHash: previous, Home: m.root.Name(), Activation: newPublication()}, nil
}
func newPublication() Publication {
	return Publication{State: PathsPending, Directory: filepath.Join(stagingDirectory, rand.Text())}
}

func (m *Manager) Activate(ctx context.Context, u Unit, owned []target.Unit) (Receipt, error) {
	r, err := m.PrepareActivation(u, owned)
	if err != nil {
		return Receipt{}, err
	}
	err = m.ResumeActivation(ctx, u, &r)
	return r, err
}
func (m *Manager) AdvanceActivation(ctx context.Context, u Unit, r *Receipt) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.validateReceipt(r); err != nil {
		return err
	}
	if u.name != r.UnitName || u.Hash() != r.InstalledHash {
		return fmt.Errorf("quadlet: resumed unit differs from recorded intent")
	}
	return m.advance(ctx, r, u.Bytes(), r.PreviousHash, r.InstalledHash, &r.Activation, true, false)
}
func (m *Manager) ResumeActivation(ctx context.Context, u Unit, r *Receipt) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.validateReceipt(r); err != nil {
		return err
	}
	if u.name != r.UnitName || u.Hash() != r.InstalledHash {
		return fmt.Errorf("quadlet: resumed unit differs from recorded intent")
	}
	for {
		if err := m.advance(ctx, r, u.Bytes(), r.PreviousHash, r.InstalledHash, &r.Activation, true, false); err != nil {
			return err
		}
		if r.Activation.State == PublicationComplete {
			return nil
		}
	}
}

// PrepareRollback can be journaled before its first filesystem effect. Repeated
// Rollback calls also reconcile a restored/absent file by redoing pending syncs.
func (m *Manager) PrepareRollback(r *Receipt) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.validateReceipt(r); err != nil {
		return err
	}
	if r.Rollback.State == "" {
		r.Rollback = newPublication()
	}
	return nil
}
func (m *Manager) AdvanceRollback(ctx context.Context, r *Receipt) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.validateReceipt(r); err != nil {
		return err
	}
	if r.Rollback.State == "" {
		return fmt.Errorf("quadlet: rollback intent required")
	}
	data, err := m.rollbackData(r)
	if err != nil {
		return err
	}
	return m.advance(ctx, r, data, r.InstalledHash, r.PreviousHash, &r.Rollback, false, true)
}
func (m *Manager) Rollback(ctx context.Context, r *Receipt) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.validateReceipt(r); err != nil {
		return err
	}
	if r.Rollback.State == "" {
		r.Rollback = newPublication()
	}
	for {
		data, err := m.rollbackData(r)
		if err != nil {
			return err
		}
		if err = m.advance(ctx, r, data, r.InstalledHash, r.PreviousHash, &r.Rollback, false, true); err != nil {
			return err
		}
		if r.Rollback.State == PublicationComplete {
			return nil
		}
	}
}
func (m *Manager) rollbackData(r *Receipt) ([]byte, error) {
	if r.PreviousHash == "" {
		return nil, nil
	}
	data, exists, err := m.readArtifact(r.PreviousPath())
	if err != nil {
		return nil, err
	}
	if !exists {
		return nil, refuseOwnership(MissingRecorded)
	}
	if digest(data) != r.PreviousHash {
		return nil, refuseOwnership(HashMismatch)
	}
	return data, nil
}
func (m *Manager) validateReceipt(r *Receipt) error {
	if r == nil || r.Home != m.root.Name() || !unitNamePattern.MatchString(r.UnitName) || !hashPattern.MatchString(r.InstalledHash) || (r.PreviousHash != "" && !hashPattern.MatchString(r.PreviousHash)) || !validPublication(r.Activation, false) || !validPublication(r.Rollback, true) {
		return fmt.Errorf("quadlet: invalid operation receipt")
	}
	return nil
}
func validPublication(p Publication, optional bool) bool {
	if p.State == "" {
		return optional && p.Directory == ""
	}
	switch p.State {
	case PathsPending, PredecessorPending, CandidatePending, RenamePending, ActiveSyncPending, SourceSyncPending, CleanupPending, StagingSyncPending, PublicationComplete:
	default:
		return false
	}
	return filepath.Dir(p.Directory) == stagingDirectory && stageTokenPattern.MatchString(filepath.Base(p.Directory))
}

func (m *Manager) advance(ctx context.Context, r *Receipt, data []byte, expected, next string, p *Publication, retain, reconcile bool) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if p.State != PathsPending {
		for _, directory := range []string{ActiveDirectory, stagingDirectory, rollbackDirectory} {
			if err := m.verifyDirectories(directory); err != nil {
				return err
			}
		}
	}
	info, err := m.root.Lstat(p.Directory)
	if err == nil {
		if err = checkDirectory(info); err != nil {
			return err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	active := filepath.Join(ActiveDirectory, r.UnitName)
	candidate := filepath.Join(p.Directory, r.UnitName)
	switch p.State {
	case PathsPending:
		if err := m.checkDirectories(); err != nil {
			return err
		}
		current, exists, err := m.readArtifact(active)
		if err != nil {
			return err
		}
		if reconcile && matches(current, exists, next) {
			p.State = ActiveSyncPending
			return nil
		}
		if err = checkExpected(current, exists, expected); err != nil {
			return err
		}
		p.State = PredecessorPending
	case PredecessorPending:
		if retain && expected != "" {
			old, exists, err := m.readArtifact(active)
			if err != nil {
				return err
			}
			if err = checkExpected(old, exists, expected); err != nil {
				return err
			}
			if err = m.retain(r.PreviousPath(), old, expected); err != nil {
				return err
			}
		}
		p.State = CandidatePending
	case CandidatePending:
		if next == "" {
			p.State = RenamePending
			return nil
		}
		if err := m.ensureDirectories(p.Directory); err != nil {
			return err
		}
		staged, exists, err := m.readArtifact(candidate)
		if err != nil {
			return err
		}
		if exists {
			if digest(staged) != next {
				return refuseOwnership(HashMismatch)
			}
			if err = m.syncArtifact(candidate, next); err != nil {
				return err
			}
		} else if err = m.writeNew(candidate, data); err != nil {
			return err
		}
		if err = m.syncDir(p.Directory); err != nil {
			return err
		}
		if err = m.validator.Validate(ctx, Candidate{Directory: filepath.Join(m.root.Name(), p.Directory), UnitName: r.UnitName}); err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return fmt.Errorf("quadlet: generator validation failed")
		}
		if err = ctx.Err(); err != nil {
			return err
		}
		staged, exists, err = m.readArtifact(candidate)
		if err != nil {
			return err
		}
		if !exists || digest(staged) != next {
			return refuseOwnership(HashMismatch)
		}
		p.State = RenamePending
	case RenamePending:
		if err := m.checkDirectories(); err != nil {
			return err
		}
		current, exists, err := m.readArtifact(active)
		if err != nil {
			return err
		}
		staged, stageExists, err := m.readArtifact(candidate)
		if err != nil {
			return err
		}
		if matches(current, exists, next) && (!stageExists || next == "") {
			p.State = ActiveSyncPending
			return nil
		}
		if err = checkExpected(current, exists, expected); err != nil {
			return err
		}
		if next == "" {
			if err = m.root.Remove(active); err != nil {
				return err
			}
		} else {
			if !stageExists || digest(staged) != next {
				return refuseOwnership(HashMismatch)
			}
			if err = m.root.Rename(candidate, active); err != nil {
				return err
			}
		}
		p.State = ActiveSyncPending
	case ActiveSyncPending:
		current, exists, err := m.readArtifact(active)
		if err != nil {
			return err
		}
		if !matches(current, exists, next) {
			return refuseOwnership(HashMismatch)
		}
		if next != "" {
			if err = m.syncArtifact(active, next); err != nil {
				return ErrPublicationUnknown
			}
		}
		if err = m.syncDir(ActiveDirectory); err != nil {
			return ErrPublicationUnknown
		}
		if next == "" {
			p.State = PublicationComplete
		} else {
			p.State = SourceSyncPending
		}
	case SourceSyncPending:
		info, err := m.root.Lstat(p.Directory)
		if errors.Is(err, os.ErrNotExist) {
			p.State = StagingSyncPending
			return nil
		}
		if err != nil {
			return err
		}
		if err = checkDirectory(info); err != nil {
			return err
		}
		if err = m.syncDir(p.Directory); err != nil {
			return ErrPublicationUnknown
		}
		p.State = CleanupPending
	case CleanupPending:
		if err := m.root.Remove(p.Directory); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		p.State = StagingSyncPending
	case StagingSyncPending:
		if err := m.syncDir(stagingDirectory); err != nil {
			return ErrPublicationUnknown
		}
		p.State = PublicationComplete
	case PublicationComplete:
		current, exists, err := m.readArtifact(active)
		if err != nil {
			return err
		}
		return checkExpected(current, exists, next)
	default:
		return fmt.Errorf("quadlet: invalid publication state")
	}
	return nil
}
func matches(data []byte, exists bool, hash string) bool {
	if hash == "" {
		return !exists
	}
	return exists && digest(data) == hash
}
func checkExpected(data []byte, exists bool, hash string) error {
	if hash == "" {
		if exists {
			return refuseOwnership(Unrecorded)
		}
		return nil
	}
	if !exists {
		return refuseOwnership(MissingRecorded)
	}
	if digest(data) != hash {
		return refuseOwnership(HashMismatch)
	}
	return nil
}

func (m *Manager) retain(path string, data []byte, hash string) error {
	existing, exists, err := m.readArtifact(path)
	if err != nil {
		return err
	}
	if exists {
		if digest(existing) != hash {
			return refuseOwnership(HashMismatch)
		}
		if err = m.syncArtifact(path, hash); err != nil {
			return err
		}
	} else {
		temporary := path + "." + rand.Text()
		if err = m.writeNew(temporary, data); err != nil {
			return err
		}
		defer m.root.Remove(temporary)
		if err = m.root.Rename(temporary, path); err != nil {
			return err
		}
	}
	// Presence and identical bytes do not prove that an earlier fsync succeeded.
	return m.syncDir(rollbackDirectory)
}
func (m *Manager) writeNew(path string, data []byte) error {
	f, err := m.root.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL|syscall.O_NOFOLLOW, 0600)
	if err != nil {
		return err
	}
	_, err = f.Write(data)
	if err == nil {
		err = m.syncFile(f)
	}
	closeErr := f.Close()
	if err != nil {
		m.root.Remove(path)
		return err
	}
	if closeErr != nil {
		m.root.Remove(path)
	}
	return closeErr
}
func (m *Manager) readArtifact(path string) ([]byte, bool, error) {
	f, exists, err := m.openArtifact(path)
	if err != nil || !exists {
		return nil, exists, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, maxUnitBytes+1))
	if err != nil {
		return nil, false, err
	}
	if len(data) > maxUnitBytes {
		return nil, false, refuseOwnership(UnsafeFile)
	}
	return data, true, nil
}
func (m *Manager) openArtifact(path string) (*os.File, bool, error) {
	info, err := m.root.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	if !info.Mode().IsRegular() || info.Size() > maxUnitBytes || info.Mode().Perm()&0022 != 0 {
		return nil, false, refuseOwnership(UnsafeFile)
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Nlink != 1 || stat.Uid != uint32(os.Geteuid()) {
		return nil, false, refuseOwnership(UnsafeFile)
	}
	f, err := m.root.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return nil, false, err
	}
	opened, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, false, err
	}
	if !os.SameFile(info, opened) {
		f.Close()
		return nil, false, refuseOwnership(HashMismatch)
	}
	return f, true, nil
}
func (m *Manager) syncArtifact(path, hash string) error {
	f, exists, err := m.openArtifact(path)
	if err != nil {
		return err
	}
	if !exists {
		return refuseOwnership(MissingRecorded)
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, maxUnitBytes+1))
	if err != nil {
		return err
	}
	if len(data) > maxUnitBytes || digest(data) != hash {
		return refuseOwnership(HashMismatch)
	}
	return m.syncFile(f)
}
func (m *Manager) syncDirectory(path string) error {
	dir, err := m.root.Open(path)
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}
func (m *Manager) checkDirectories() error {
	for _, dir := range []string{ActiveDirectory, stagingDirectory, rollbackDirectory} {
		if err := m.ensureDirectories(dir); err != nil {
			return err
		}
	}
	return nil
}
func checkDirectory(info os.FileInfo) error {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0022 != 0 || !ok || stat.Uid != uint32(os.Geteuid()) {
		return refuseOwnership(UnsafeParent)
	}
	return nil
}
func (m *Manager) verifyDirectories(path string) error {
	current := ""
	for _, part := range strings.Split(path, "/") {
		current = filepath.Join(current, part)
		info, err := m.root.Lstat(current)
		if err != nil {
			return err
		}
		if err = checkDirectory(info); err != nil {
			return err
		}
	}
	return nil
}
func (m *Manager) ensureDirectories(path string) error {
	current := ""
	for _, part := range strings.Split(path, "/") {
		current = filepath.Join(current, part)
		info, err := m.root.Lstat(current)
		if errors.Is(err, os.ErrNotExist) {
			if err = m.root.Mkdir(current, 0700); err != nil {
				return err
			}
			info, err = m.root.Lstat(current)
		}
		if err != nil {
			return err
		}
		if err = checkDirectory(info); err != nil {
			return err
		}
		// Retry this even for existing entries: existence is not a durability record.
		if err = m.syncDir(filepath.Dir(current)); err != nil {
			return err
		}
	}
	return nil
}
