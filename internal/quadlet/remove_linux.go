package quadlet

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"

	"github.com/ShaulLavo/brine/internal/plan"
)

func (m *Manager) verifyRemove(ctx context.Context, name, hash string) error {
	return m.verifyRemoval(ctx, name, hash, false)
}
func (m *Manager) verifyRemoval(ctx context.Context, name, hash string, archived bool) error {
	if !unitNamePattern.MatchString(name) || !hashPattern.MatchString(hash) {
		return errors.New("quadlet: invalid removal intent")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := m.checkDirectories(); err != nil {
		return err
	}
	data, present, err := m.readArtifact(filepath.Join(ActiveDirectory, name))
	if err != nil {
		return err
	}
	return verifyRemovalBytes(data, present, hash, archived)
}
func verifyRemovalBytes(data []byte, present bool, hash string, archived bool) error {
	if !present {
		if archived {
			return refuseOwnership(Unrecorded)
		}
		return nil
	}
	if err := checkExpected(data, present, hash); err != nil {
		return err
	}
	line, _, _ := bytes.Cut(data, []byte("\n"))
	if !strings.HasPrefix(string(line), marker) || !hashPattern.MatchString(strings.TrimPrefix(string(line), marker)) {
		return refuseOwnership(Unrecorded)
	}
	for _, line := range strings.Split(string(data), "\n") {
		key, _, ok := strings.Cut(strings.TrimSpace(line), "=")
		key = strings.TrimSpace(key)
		if !archived && ok && (key == "Volume" || key == "Mount" || key == "ReadWritePaths") {
			return plan.ErrPersistentData
		}
	}
	return nil
}

func (m *Manager) VerifyRemove(ctx context.Context, name, hash string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.verifyRemove(ctx, name, hash)
}

func (m *Manager) Remove(ctx context.Context, name, hash string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.verifyRemove(ctx, name, hash); err != nil {
		return err
	}
	return m.replace(ctx, name, hash, "", nil, false)
}

// VerifyArchiveRemove requires the exact recorded persistent artifact to remain
// present. Callers must hold archive fences and independently prove quiescence.
func (m *Manager) VerifyArchiveRemove(ctx context.Context, name, hash string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.verifyRemoval(ctx, name, hash, true)
}

// RemoveArchived removes only the owned unit, never its mounted data.
func (m *Manager) RemoveArchived(ctx context.Context, name, hash string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.verifyRemoval(ctx, name, hash, true); err != nil {
		return err
	}
	return m.replace(ctx, name, hash, "", nil, false)
}

// InspectArchiveRemove distinguishes verified presence from affirmative absence.
// Absence grants no ordinary remove authority; only a retained archive intent can
// adopt it. Present but changed artifacts always refuse.
func (m *Manager) InspectArchiveRemove(ctx context.Context, name, hash string) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !unitNamePattern.MatchString(name) || !hashPattern.MatchString(hash) {
		return false, refuseOwnership(Unrecorded)
	}
	if err := ctx.Err(); err != nil {
		return false, err
	}
	for _, directory := range []string{ActiveDirectory, stagingDirectory} {
		if err := m.verifyDirectories(directory); err != nil {
			return false, err
		}
	}
	data, present, err := m.readArtifact(filepath.Join(ActiveDirectory, name))
	if err != nil || !present {
		return false, err
	}
	return true, verifyRemovalBytes(data, present, hash, true)
}

// InspectArchiveRemove opens an existing manager namespace without creating
// directories or invoking a validator, reload or any effect-replay path.
func InspectArchiveRemove(ctx context.Context, home, name, hash string) (present bool, err error) {
	root, err := os.OpenRoot(home)
	if err != nil {
		return false, err
	}
	defer func() { err = errors.Join(err, root.Close()) }()
	manager := Manager{root: root}
	return manager.InspectArchiveRemove(ctx, name, hash)
}
