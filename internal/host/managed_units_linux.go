//go:build linux

package host

import (
	"bytes"
	"context"
	"crypto/rand"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"

	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/ops"
	"golang.org/x/sys/unix"
)

const managedServiceDirectory = ".config/systemd/user"
const managedServiceStaging = ".local/state/brine/engine-units"
const managedServiceLimit = 64 << 10

var errManagedService = errors.New("managed engine service ownership refused")
var errManagedServiceUnknown = errors.New("managed engine service publication requires inspection")

type managedServiceChange struct {
	binding  data.ReplicaBindingID
	name     string
	raw      []byte
	wanted   string
	previous string
}

// The host constructs changes from the expected runtime and immutable previous
// release loaded under its operation lock. Requests never supply artifact paths
// or predecessor hashes. This artifact check grants no native start authority.
func newManagedServiceChange(runtime data.EngineRuntime, previous ops.Release) (managedServiceChange, error) {
	if runtime.Validate() != nil {
		return managedServiceChange{}, errManagedService
	}
	raw, err := EngineServiceBytes(runtime)
	if err != nil || len(raw) == 0 || len(raw) > managedServiceLimit || managedServiceHash(raw) != runtime.UnitSHA256 {
		return managedServiceChange{}, errManagedService
	}
	change := managedServiceChange{binding: runtime.BindingID, name: "brine-engine-" + string(runtime.BindingID) + ".service", raw: raw, wanted: runtime.UnitSHA256}
	for _, unit := range previous.Units {
		if unit.Name != change.name {
			continue
		}
		hash, ok := strings.CutPrefix(unit.Hash, "sha256:")
		if !ok || !data.ValidCatalogHash(hash) || change.previous != "" {
			return managedServiceChange{}, errManagedService
		}
		change.previous = hash
	}
	return change, nil
}

type managedUnits struct {
	mu   sync.Mutex
	root *os.Root
}

func openManagedUnits(home string) (*managedUnits, error) {
	if !filepath.IsAbs(home) || filepath.Clean(home) != home {
		return nil, errManagedService
	}
	if err := managedHomeAncestors(home); err != nil {
		return nil, err
	}
	root, err := engineOpenRoot(home)
	if err != nil {
		return nil, errManagedService
	}
	units := &managedUnits{root: root}
	if err := units.directory(managedServiceDirectory, false); err != nil {
		_ = root.Close()
		return nil, err
	}
	return units, nil
}

func (m *managedUnits) Close() error { return m.root.Close() }

func (m *managedUnits) directories(create bool) error {
	info, err := m.root.Stat(".")
	if err != nil || !managedAncestorNode(info, false) {
		return errManagedService
	}
	for _, directory := range []string{managedServiceDirectory, managedServiceStaging} {
		if err := m.directory(directory, create); err != nil {
			return err
		}
	}
	return nil
}

func (m *managedUnits) directory(relative string, create bool) error {
	current := ""
	for _, part := range strings.Split(relative, string(filepath.Separator)) {
		current = filepath.Join(current, part)
		if err := m.directoryEntry(current, create, current == relative); err != nil {
			return err
		}
	}
	return nil
}

func (m *managedUnits) directoryEntry(relative string, create, private bool) error {
	info, err := m.root.Lstat(relative)
	if errors.Is(err, os.ErrNotExist) && create {
		if err = m.root.Mkdir(relative, 0700); err != nil {
			return errManagedService
		}
		if err = m.syncDirectory(filepath.Dir(relative)); err != nil {
			return err
		}
		info, err = m.root.Lstat(relative)
	}
	if err != nil || !managedAncestorNode(info, false) || private && !managedServiceNode(info, true) {
		return errManagedService
	}
	return nil
}

func managedHomeAncestors(home string) error {
	current := home
	for {
		info, err := os.Lstat(current)
		if err != nil || !managedAncestorNode(info, current != home) {
			return errManagedService
		}
		if current == string(filepath.Separator) {
			return nil
		}
		current = filepath.Dir(current)
	}
}

func managedAncestorNode(info os.FileInfo, allowRoot bool) bool {
	stat, ok := info.Sys().(*syscall.Stat_t)
	return ok && info.IsDir() && info.Mode()&os.ModeSymlink == 0 && info.Mode().Perm()&0022 == 0 && (int(stat.Uid) == os.Geteuid() || allowRoot && stat.Uid == 0)
}

func managedServiceNode(info os.FileInfo, directory bool) bool {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || int64(stat.Uid) != int64(os.Geteuid()) {
		return false
	}
	if directory {
		return info.Mode() == os.ModeDir|0700
	}
	return info.Mode() == 0600 && stat.Nlink == 1
}

func (m *managedUnits) read(ctx context.Context, relative string) ([]byte, bool, error) {
	if ctx.Err() != nil {
		return nil, false, ctx.Err()
	}
	info, err := m.root.Lstat(relative)
	if errors.Is(err, os.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil || !managedServiceNode(info, false) || info.Size() < 0 || info.Size() > managedServiceLimit {
		return nil, false, errManagedService
	}
	file, err := m.root.OpenFile(relative, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, false, errManagedService
	}
	defer func() { _ = file.Close() }()
	opened, err := file.Stat()
	if err != nil || !os.SameFile(info, opened) || !managedServiceNode(opened, false) || opened.Size() != info.Size() {
		return nil, false, errManagedService
	}
	raw, err := io.ReadAll(io.LimitReader(file, managedServiceLimit+1))
	final, finalErr := m.root.Lstat(relative)
	if err != nil || finalErr != nil || !os.SameFile(info, final) || int64(len(raw)) != info.Size() || !managedServiceNode(final, false) || ctx.Err() != nil {
		return nil, false, errManagedService
	}
	return raw, true, nil
}

func (m *managedUnits) stage(ctx context.Context, change managedServiceChange) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !change.valid() || m.directories(true) != nil {
		return errManagedService
	}
	path := filepath.Join(managedServiceStaging, change.name+"."+change.wanted)
	raw, present, err := m.read(ctx, path)
	if err != nil {
		return err
	}
	if present {
		if !bytes.Equal(raw, change.raw) {
			return errManagedService
		}
		return nil
	}
	return m.write(ctx, path, change.raw, "")
}

func (c managedServiceChange) valid() bool {
	return data.ValidID(string(c.binding)) && c.name == "brine-engine-"+string(c.binding)+".service" && len(c.raw) > 0 && len(c.raw) <= managedServiceLimit && data.ValidCatalogHash(c.wanted) && managedServiceHash(c.raw) == c.wanted && (c.previous == "" || data.ValidCatalogHash(c.previous))
}

func (m *managedUnits) verify(ctx context.Context, change managedServiceChange, allowed ...string) error {
	if !change.valid() || len(allowed) == 0 || len(allowed) > 2 || m.directories(false) != nil {
		return errManagedService
	}
	raw, present, err := m.read(ctx, filepath.Join(managedServiceDirectory, change.name))
	if err != nil {
		return err
	}
	for _, hash := range allowed {
		if hash != change.previous && hash != change.wanted {
			return errManagedService
		}
		if !present && hash == "" || present && managedServiceHash(raw) == hash {
			return nil
		}
	}
	return errManagedService
}

func (m *managedUnits) install(ctx context.Context, change managedServiceChange) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.verify(ctx, change, change.previous, change.wanted); err != nil {
		return err
	}
	active := filepath.Join(managedServiceDirectory, change.name)
	current, present, err := m.read(ctx, active)
	if err != nil {
		return err
	}
	if present && managedServiceHash(current) == change.wanted {
		return nil
	}
	staged, stagedPresent, err := m.read(ctx, filepath.Join(managedServiceStaging, change.name+"."+change.wanted))
	if err != nil || !stagedPresent || !bytes.Equal(staged, change.raw) {
		return errManagedService
	}
	if present {
		if err := m.retain(ctx, change, current); err != nil {
			return err
		}
	}
	if err := m.write(ctx, active, staged, change.previous); err != nil {
		return err
	}
	if err := m.verify(ctx, change, change.wanted); err != nil {
		return errManagedServiceUnknown
	}
	return nil
}

func (m *managedUnits) retain(ctx context.Context, change managedServiceChange, raw []byte) error {
	path := filepath.Join(managedServiceStaging, change.name+"."+change.previous)
	retained, present, err := m.read(ctx, path)
	if err != nil {
		return err
	}
	if present {
		if !bytes.Equal(retained, raw) {
			return errManagedService
		}
		return nil
	}
	return m.write(ctx, path, raw, "")
}

func (m *managedUnits) restore(ctx context.Context, change managedServiceChange) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.verify(ctx, change, change.previous, change.wanted); err != nil {
		return err
	}
	active := filepath.Join(managedServiceDirectory, change.name)
	raw, present, err := m.read(ctx, active)
	if err != nil || !present && change.previous != "" {
		return errManagedService
	}
	if !present || managedServiceHash(raw) == change.previous {
		return nil
	}
	if change.previous == "" {
		if err := m.root.Remove(active); err != nil {
			return errManagedServiceUnknown
		}
		if err := m.syncDirectory(managedServiceDirectory); err != nil {
			return err
		}
		return m.verify(ctx, change, "")
	}
	retained, exists, err := m.read(ctx, filepath.Join(managedServiceStaging, change.name+"."+change.previous))
	if err != nil || !exists || managedServiceHash(retained) != change.previous {
		return errManagedService
	}
	if err := m.write(ctx, active, retained, change.wanted); err != nil {
		return err
	}
	if err := m.verify(ctx, change, change.previous); err != nil {
		return errManagedServiceUnknown
	}
	return nil
}

func (m *managedUnits) write(ctx context.Context, path string, raw []byte, expected string) error {
	if ctx.Err() != nil || len(raw) == 0 || len(raw) > managedServiceLimit {
		return errManagedService
	}
	directory := filepath.Dir(path)
	temporary := filepath.Join(directory, ".brine-engine-"+rand.Text()+".tmp")
	file, err := m.root.OpenFile(temporary, os.O_WRONLY|os.O_CREATE|os.O_EXCL|syscall.O_NOFOLLOW, 0600)
	if err != nil {
		return errManagedService
	}
	retained := false
	defer func() {
		if !retained {
			_ = m.root.Remove(temporary)
		}
	}()
	_, writeErr := file.Write(raw)
	err = errors.Join(writeErr, file.Sync(), file.Close(), ctx.Err())
	if err != nil {
		return errManagedService
	}
	current, present, err := m.read(ctx, path)
	if err != nil || present && managedServiceHash(current) != expected || !present && expected != "" {
		return errManagedService
	}
	parent, err := engineOpenMember(m.root, directory, unix.O_RDONLY|unix.O_DIRECTORY, 0)
	if err != nil {
		return errManagedService
	}
	defer func() { _ = parent.Close() }()
	flags := uint(unix.RENAME_NOREPLACE)
	if expected != "" {
		flags = unix.RENAME_EXCHANGE
	}
	if err := unix.Renameat2(int(parent.Fd()), filepath.Base(temporary), int(parent.Fd()), filepath.Base(path), flags); err != nil {
		return errManagedService
	}
	if expected != "" {
		// Exchange retains the displaced file. A raced foreign predecessor is
		// preserved for inspection rather than overwritten or deleted.
		retained = true
		displaced, present, err := m.read(ctx, temporary)
		if err != nil || !present || managedServiceHash(displaced) != expected {
			return errManagedServiceUnknown
		}
		if err := unix.Unlinkat(int(parent.Fd()), filepath.Base(temporary), 0); err != nil {
			return errManagedServiceUnknown
		}
		retained = false
	}
	if err := m.syncDirectory(directory); err != nil {
		return errManagedServiceUnknown
	}
	return nil
}

func (m *managedUnits) syncDirectory(path string) error {
	file, err := m.root.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return errManagedServiceUnknown
	}
	defer func() { _ = file.Close() }()
	if err := file.Sync(); err != nil {
		return errManagedServiceUnknown
	}
	return nil
}
