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
)

const (
	ActiveDirectory  = ".config/containers/systemd"
	stagingDirectory = ".local/share/brine/quadlet/staging"
	maxUnitBytes     = 4 << 20
)

// Candidate is isolated from active Quadlet files. The adapter must bound its
// generator dry-run and verify the expected service without logging env values.
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

// Manager has no operation state. Callers journal intent and hold D1's host lock
// across each operation. Empty hashes mean absence, never unknown ownership.
// The per-unit .brine-prev slot and .brine-*.tmp namespace are reserved outputs.
type Manager struct {
	mu        sync.Mutex
	root      *os.Root
	validator Validator
	syncDir   func(string) error
	syncFile  func(*os.File) error
	after     func(string, string) error
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

func validHash(hash string) bool       { return hash == "" || hashPattern.MatchString(hash) }
func previousPath(name string) string  { return filepath.Join(ActiveDirectory, name+".brine-prev") }
func temporaryName(name string) string { return "." + name + ".brine-" + rand.Text() + ".tmp" }
func isTemporary(entry, name string) bool {
	prefix := "." + name + ".brine-"
	if !strings.HasPrefix(entry, prefix) || !strings.HasSuffix(entry, ".tmp") {
		return false
	}
	return stageTokenPattern.MatchString(strings.TrimSuffix(strings.TrimPrefix(entry, prefix), ".tmp"))
}

// Install receives a rendered unit and its committed predecessor hash from
// BrineState, not inventory. The desired hash is inseparable from Unit's bytes.
func (m *Manager) Install(ctx context.Context, u Unit, oldHash string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !unitNamePattern.MatchString(u.name) || u.content == "" || len(u.content) > maxUnitBytes || !validHash(oldHash) {
		return fmt.Errorf("quadlet: invalid install intent")
	}
	return m.replace(ctx, u.name, oldHash, u.Hash(), u.Bytes(), true)
}

// Rollback repeats the same reconciliation with installed/previous hashes
// swapped. The retained slot must match the committed previous hash exactly.
func (m *Manager) Rollback(ctx context.Context, name, installedHash, previousHash string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !unitNamePattern.MatchString(name) || !hashPattern.MatchString(installedHash) || !validHash(previousHash) {
		return fmt.Errorf("quadlet: invalid rollback intent")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := m.checkDirectories(); err != nil {
		return err
	}
	var data []byte
	if previousHash != "" {
		var exists bool
		var err error
		data, exists, err = m.readArtifact(previousPath(name))
		if err != nil {
			return err
		}
		if err = checkExpected(data, exists, previousHash); err != nil {
			return err
		}
	}
	return m.replace(ctx, name, installedHash, previousHash, data, false)
}

func (m *Manager) replace(ctx context.Context, name, oldHash, newHash string, data []byte, retain bool) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := m.checkDirectories(); err != nil {
		return err
	}
	active := filepath.Join(ActiveDirectory, name)
	current, exists, err := m.readArtifact(active)
	if err != nil {
		return err
	}
	done := matches(current, exists, newHash)
	// Drift must cause no cleanup writes, even when abandoned temps are present.
	if !done {
		if err = checkExpected(current, exists, oldHash); err != nil {
			return err
		}
	}
	if retain && done && oldHash != "" && oldHash != newHash {
		prior, present, err := m.readArtifact(previousPath(name))
		if err != nil {
			return err
		}
		if err = checkExpected(prior, present, oldHash); err != nil {
			return err
		}
	}
	if err = m.cleanTemporaryFiles(name); err != nil {
		return err
	}
	if done {
		if retain && oldHash != "" && oldHash != newHash {
			if err = m.syncArtifact(previousPath(name), oldHash); err != nil {
				return err
			}
		}
		return m.finish(active, newHash)
	}
	if newHash != "" {
		if err = m.validate(ctx, name, data, newHash); err != nil {
			return err
		}
	}
	if err = ctx.Err(); err != nil {
		return err
	}
	if err = m.verifyDirectories(ActiveDirectory); err != nil {
		return err
	}
	current, exists, err = m.readArtifact(active)
	if err != nil {
		return err
	}
	if err = checkExpected(current, exists, oldHash); err != nil {
		return err
	}
	candidate := ""
	if newHash != "" {
		candidate, err = m.writeTemporary(ActiveDirectory, name, data)
		if err != nil {
			return err
		}
	}
	if retain && oldHash != "" {
		if err = m.retain(name, current, oldHash); err != nil {
			return err
		}
	}
	if err = ctx.Err(); err != nil {
		return err
	}
	if err = m.verifyDirectories(ActiveDirectory); err != nil {
		return err
	}
	current, exists, err = m.readArtifact(active)
	if err != nil {
		return err
	}
	if err = checkExpected(current, exists, oldHash); err != nil {
		return err
	}
	if newHash == "" {
		err = m.remove(active)
	} else {
		staged, present, readErr := m.readArtifact(candidate)
		if readErr != nil {
			return readErr
		}
		if err = checkExpected(staged, present, newHash); err != nil {
			return err
		}
		err = m.rename(candidate, active)
	}
	if err != nil {
		return ErrPublicationUnknown
	}
	return m.finish(active, newHash)
}
func (m *Manager) finish(active, hash string) error {
	if hash != "" {
		if err := m.syncArtifact(active, hash); err != nil {
			return errors.Join(ErrPublicationUnknown, err)
		}
	}
	if err := m.syncDir(ActiveDirectory); err != nil {
		return ErrPublicationUnknown
	}
	data, exists, err := m.readArtifact(active)
	if err != nil {
		return err
	}
	return checkExpected(data, exists, hash)
}
func (m *Manager) retain(name string, data []byte, hash string) error {
	path := previousPath(name)
	prior, exists, err := m.readArtifact(path)
	if err != nil {
		return err
	}
	if exists && digest(prior) == hash {
		if err = m.syncArtifact(path, hash); err != nil {
			return err
		}
	} else {
		// A recorded active unit owns this reserved backup slot. Replace it atomically
		// so a crash cannot leave a partial predecessor or lose the current unit.
		temporary, err := m.writeTemporary(ActiveDirectory, name, data)
		if err != nil {
			return err
		}
		if err = m.rename(temporary, path); err != nil {
			return err
		}
	}
	return m.syncDir(ActiveDirectory)
}
func (m *Manager) validate(ctx context.Context, name string, data []byte, hash string) error {
	directory := filepath.Join(stagingDirectory, temporaryName(name))
	if err := m.ensureDirectories(directory); err != nil {
		return err
	}
	temporary, err := m.writeTemporary(directory, name, data)
	if err != nil {
		return err
	}
	candidate := filepath.Join(directory, name)
	if err = m.rename(temporary, candidate); err != nil {
		return err
	}
	if err = m.syncDir(directory); err != nil {
		return err
	}
	if err = m.validator.Validate(ctx, Candidate{Directory: filepath.Join(m.root.Name(), directory), UnitName: name}); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return fmt.Errorf("quadlet: generator validation failed")
	}
	if err = ctx.Err(); err != nil {
		return err
	}
	if err = m.verifyDirectories(directory); err != nil {
		return err
	}
	staged, exists, err := m.readArtifact(candidate)
	if err != nil {
		return err
	}
	if err = checkExpected(staged, exists, hash); err != nil {
		return err
	}
	if err = m.remove(candidate); err != nil {
		return err
	}
	if err = m.remove(directory); err != nil {
		return err
	}
	return m.syncDir(stagingDirectory)
}
func (m *Manager) cleanTemporaryFiles(name string) error {
	for _, directory := range []string{ActiveDirectory, stagingDirectory} {
		entries, err := m.readDirectory(directory)
		if err != nil {
			return err
		}
		for _, entry := range entries {
			if !isTemporary(entry.Name(), name) {
				continue
			}
			path := filepath.Join(directory, entry.Name())
			if directory == stagingDirectory {
				if err = m.verifyDirectories(path); err != nil {
					return err
				}
				children, err := m.readDirectory(path)
				if err != nil {
					return err
				}
				for _, child := range children {
					if child.Name() != name && !isTemporary(child.Name(), name) {
						return refuseOwnership(Unrecorded)
					}
				}
				for _, child := range children {
					if err = m.removeTemporary(filepath.Join(path, child.Name())); err != nil {
						return err
					}
				}
				if err = m.remove(path); err != nil {
					return err
				}
			} else if err = m.removeTemporary(path); err != nil {
				return err
			}
		}
		if err = m.syncDir(directory); err != nil {
			return err
		}
	}
	return nil
}
func (m *Manager) readDirectory(path string) ([]os.DirEntry, error) {
	f, err := m.root.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return f.ReadDir(-1)
}
func (m *Manager) removeTemporary(path string) error {
	f, exists, err := m.openArtifact(path)
	if err != nil {
		return err
	}
	if !exists {
		return nil
	}
	if err = f.Close(); err != nil {
		return err
	}
	return m.remove(path)
}
func (m *Manager) writeTemporary(directory, name string, data []byte) (string, error) {
	path := filepath.Join(directory, temporaryName(name))
	f, err := m.root.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL|syscall.O_NOFOLLOW, 0600)
	if err != nil {
		return "", err
	}
	defer f.Close()
	if err = m.checkpoint("file-created", path); err != nil {
		return path, err
	}
	n, err := f.Write(data)
	if err != nil {
		return path, err
	}
	if n != len(data) {
		return path, io.ErrShortWrite
	}
	if err = m.checkpoint("file-written", path); err != nil {
		return path, err
	}
	if err = m.syncFile(f); err != nil {
		return path, err
	}
	if err = m.checkpoint("file-synced", path); err != nil {
		return path, err
	}
	return path, f.Close()
}
func (m *Manager) remove(path string) error {
	if err := m.root.Remove(path); err != nil {
		return err
	}
	return m.checkpoint("removed", path)
}
func (m *Manager) rename(source, destination string) error {
	if err := m.root.Rename(source, destination); err != nil {
		return err
	}
	return m.checkpoint("renamed", destination)
}
func (m *Manager) checkpoint(event, path string) error {
	if m.after != nil {
		return m.after(event, path)
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
	if err = m.syncFile(f); err != nil {
		return err
	}
	return m.checkpoint("file-synced", path)
}
func (m *Manager) syncDirectory(path string) error {
	dir, err := m.root.Open(path)
	if err != nil {
		return err
	}
	defer dir.Close()
	if err = dir.Sync(); err != nil {
		return err
	}
	return m.checkpoint("directory-synced", path)
}
func (m *Manager) checkDirectories() error {
	for _, dir := range []string{ActiveDirectory, stagingDirectory} {
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
			if err = m.checkpoint("directory-created", current); err != nil {
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

// Stage validates isolated candidate bytes without touching the active unit.
// Install repeats validation and ownership checks before publication.
func (m *Manager) Stage(ctx context.Context, u Unit) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !unitNamePattern.MatchString(u.name) || u.content == "" || len(u.content) > maxUnitBytes {
		return fmt.Errorf("quadlet: invalid stage intent")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := m.checkDirectories(); err != nil {
		return err
	}
	return m.validate(ctx, u.name, u.Bytes(), u.Hash())
}
