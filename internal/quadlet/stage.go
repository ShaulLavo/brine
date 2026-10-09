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
	"strings"
	"sync"
	"syscall"
)

const (
	ActiveDirectory   = ".config/containers/systemd"
	stagingDirectory  = ".local/share/brine/quadlet/staging"
	rollbackDirectory = ".local/share/brine/quadlet/rollback"
	maxUnitBytes      = 4 << 20
)

// Candidate isolates the single rendered unit from active units. A real adapter
// runs podman-system-generator --user --dryrun with QUADLET_UNIT_DIRS set to
// Directory, checks generation of UnitName's service, and bounds time/output.
// It must not log literal environment settings or reload the user manager.
type Candidate struct {
	Directory string
	UnitName  string
}
type Validator interface {
	Validate(context.Context, Candidate) error
}

// ErrPublicationUnknown means rename or removal completed but directory sync
// failed. Inspect the active artifact before deciding whether to retry.
var ErrPublicationUnknown = errors.New("quadlet: publication requires reconciliation")

// Receipt describes an installed artifact and its retained predecessor. A zero
// receipt is invalid. Rollback refuses drift rather than replacing a newer unit.
type Receipt struct{ name, installedHash, previousHash, home string }

func (r Receipt) PreviousPath() string {
	if r.previousHash == "" {
		return ""
	}
	return filepath.Join(rollbackDirectory, r.name+"-"+strings.TrimPrefix(r.previousHash, "sha256:"))
}

// Manager uses an injectable runner home, including a D7 operator-owned home
// with writable .config and .local children. Callers must hold D1's host mutation
// lock across managers/processes and systemd activation. Its mutex serializes
// local calls only. Installation never reloads, starts or enables any service.
type Manager struct {
	mu        sync.Mutex
	root      *os.Root
	validator Validator
	syncDir   func(string) error
}

func NewManager(home string, v Validator) (*Manager, error) {
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
	for _, dir := range []string{ActiveDirectory, stagingDirectory, rollbackDirectory} {
		if err = ensureDirectories(root, dir); err != nil {
			root.Close()
			return nil, err
		}
	}
	m := &Manager{root: root, validator: v}
	m.syncDir = m.syncDirectory
	return m, nil
}
func (m *Manager) Close() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.root.Close()
}

func (m *Manager) Activate(ctx context.Context, u Unit) (Receipt, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if u.name == "" || !owned(u.Bytes()) {
		return Receipt{}, fmt.Errorf("quadlet: rendered unit required")
	}
	if err := ctx.Err(); err != nil {
		return Receipt{}, err
	}
	if err := m.checkDirectories(); err != nil {
		return Receipt{}, err
	}
	old, exists, err := m.readOwned(filepath.Join(ActiveDirectory, u.name))
	if err != nil {
		return Receipt{}, err
	}
	r := Receipt{name: u.name, installedHash: u.Hash(), home: m.root.Name()}
	if exists {
		r.previousHash = digest(old)
		if err = m.retain(r.PreviousPath(), old); err != nil {
			return Receipt{}, err
		}
	}
	if err = m.install(ctx, u.name, u.Bytes(), old, exists); err != nil {
		return r, err
	}
	return r, nil
}

func (m *Manager) Rollback(ctx context.Context, r Receipt) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if r.home != m.root.Name() || r.name == "" || !hashPattern.MatchString(r.installedHash) || (r.previousHash != "" && !hashPattern.MatchString(r.previousHash)) {
		return fmt.Errorf("quadlet: invalid rollback receipt")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := m.checkDirectories(); err != nil {
		return err
	}
	current, exists, err := m.readOwned(filepath.Join(ActiveDirectory, r.name))
	if err != nil {
		return err
	}
	if !exists {
		if r.previousHash == "" {
			return nil
		}
		return fmt.Errorf("quadlet: active artifact missing")
	}
	if r.previousHash != "" && digest(current) == r.previousHash {
		return nil
	}
	if digest(current) != r.installedHash {
		return fmt.Errorf("quadlet: active artifact drift")
	}
	if r.previousHash == "" {
		if err := m.root.Remove(filepath.Join(ActiveDirectory, r.name)); err != nil {
			return err
		}
		if err := m.syncDir(ActiveDirectory); err != nil {
			return ErrPublicationUnknown
		}
		return nil
	}
	previous, exists, err := m.readOwned(r.PreviousPath())
	if err != nil {
		return err
	}
	if !exists || digest(previous) != r.previousHash {
		return fmt.Errorf("quadlet: rollback artifact missing or changed")
	}
	return m.install(ctx, r.name, previous, current, true)
}

func (m *Manager) retain(path string, data []byte) error {
	existing, exists, err := m.readOwned(path)
	if err != nil {
		return err
	}
	if exists {
		if string(existing) != string(data) {
			return fmt.Errorf("quadlet: rollback artifact drift")
		}
		return nil
	}
	temporary := path + "." + rand.Text()
	if err = m.writeNew(temporary, data); err != nil {
		return err
	}
	defer m.root.Remove(temporary)
	if err = m.root.Rename(temporary, path); err != nil {
		return err
	}
	return m.syncDir(rollbackDirectory)
}

func (m *Manager) install(ctx context.Context, name string, data, expected []byte, expectedExists bool) error {
	dir := filepath.Join(stagingDirectory, rand.Text())
	if err := m.root.Mkdir(dir, 0700); err != nil {
		return err
	}
	defer m.root.RemoveAll(dir)
	stage := filepath.Join(dir, name)
	if err := m.writeNew(stage, data); err != nil {
		return err
	}
	if err := m.validator.Validate(ctx, Candidate{Directory: filepath.Join(m.root.Name(), dir), UnitName: name}); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return fmt.Errorf("quadlet: generator validation failed")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	staged, exists, err := m.readOwned(stage)
	if err != nil {
		return err
	}
	if !exists || string(staged) != string(data) {
		return fmt.Errorf("quadlet: validated artifact changed")
	}
	if err := m.checkDirectories(); err != nil {
		return err
	}
	active := filepath.Join(ActiveDirectory, name)
	current, exists, err := m.readOwned(active)
	if err != nil {
		return err
	}
	if exists != expectedExists || string(current) != string(expected) {
		return fmt.Errorf("quadlet: active artifact changed during validation")
	}
	if err = m.root.Rename(stage, active); err != nil {
		return err
	}
	// A sync error after rename has an unknown durability outcome. The caller
	// receives its receipt with the error and must reconcile, not blindly retry.
	if err := m.syncDir(ActiveDirectory); err != nil {
		return ErrPublicationUnknown
	}
	return nil
}

func (m *Manager) writeNew(path string, data []byte) error {
	f, err := m.root.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL|syscall.O_NOFOLLOW, 0600)
	if err != nil {
		return err
	}
	_, err = f.Write(data)
	if err == nil {
		err = f.Sync()
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
func (m *Manager) readOwned(path string) ([]byte, bool, error) {
	info, err := m.root.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	if !info.Mode().IsRegular() || info.Size() > maxUnitBytes {
		return nil, false, fmt.Errorf("quadlet: refusing non-regular artifact")
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Nlink != 1 {
		return nil, false, fmt.Errorf("quadlet: refusing linked artifact")
	}
	f, err := m.root.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return nil, false, err
	}
	defer f.Close()
	opened, err := f.Stat()
	if err != nil {
		return nil, false, err
	}
	if !os.SameFile(info, opened) {
		return nil, false, fmt.Errorf("quadlet: artifact changed while opening")
	}
	data, err := io.ReadAll(io.LimitReader(f, maxUnitBytes+1))
	if err != nil {
		return nil, false, err
	}
	if len(data) > maxUnitBytes || !owned(data) {
		return nil, false, fmt.Errorf("quadlet: refusing artifact without Brine ownership")
	}
	return data, true, nil
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
		if err := ensureDirectories(m.root, dir); err != nil {
			return err
		}
	}
	return nil
}

func ensureDirectories(root *os.Root, path string) error {
	current := ""
	for _, part := range strings.Split(path, "/") {
		current = filepath.Join(current, part)
		info, err := root.Lstat(current)
		if errors.Is(err, os.ErrNotExist) {
			if err = root.Mkdir(current, 0700); err != nil {
				return err
			}
			continue
		}
		if err != nil {
			return err
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("quadlet: refusing symlink or non-directory parent")
		}
	}
	return nil
}
