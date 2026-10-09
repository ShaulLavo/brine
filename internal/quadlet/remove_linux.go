package quadlet

import (
	"bytes"
	"context"
	"errors"
	"path/filepath"
	"strings"

	"github.com/ShaulLavo/brine/internal/plan"
)

func (m *Manager) verifyRemove(ctx context.Context, name, hash string) error {
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
	if !present {
		return nil
	}
	if err = checkExpected(data, present, hash); err != nil {
		return err
	}
	line, _, _ := bytes.Cut(data, []byte("\n"))
	if !strings.HasPrefix(string(line), marker) || !hashPattern.MatchString(strings.TrimPrefix(string(line), marker)) {
		return refuseOwnership(Unrecorded)
	}
	for _, line := range strings.Split(string(data), "\n") {
		key, _, ok := strings.Cut(strings.TrimSpace(line), "=")
		if ok && (key == "Volume" || key == "Mount" || key == "ReadWritePaths") {
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
