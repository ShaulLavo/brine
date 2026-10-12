//go:build linux

package quadlet

import (
	"context"
	"errors"
)

// RemoveRetained preserves the exact owned frontend bytes for compensation.
func (m *Manager) RemoveRetained(ctx context.Context, name, hash string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.verifyRemove(ctx, name, hash); err != nil {
		return err
	}
	return m.replace(ctx, name, hash, "", nil, true)
}
func (m *Manager) RestoreRemoved(ctx context.Context, name, hash string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !unitNamePattern.MatchString(name) || !hashPattern.MatchString(hash) {
		return errors.New("quadlet: invalid frontend restoration")
	}
	if err := m.checkDirectories(); err != nil {
		return err
	}
	raw, present, err := m.readArtifact(previousPath(name))
	if err != nil {
		return err
	}
	if err := checkExpected(raw, present, hash); err != nil {
		return err
	}
	if err := verifyRemovalBytes(raw, present, hash, false); err != nil {
		return err
	}
	return m.replace(ctx, name, "", hash, raw, false)
}
