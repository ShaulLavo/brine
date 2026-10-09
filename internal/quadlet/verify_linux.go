package quadlet

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
)

// VerifyCurrent opens a read-only ownership probe without creating or syncing
// directories. Missing or unsafe parent directories refuse.
func VerifyCurrent(ctx context.Context, home, name string, hashes ...string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	root, err := os.OpenRoot(home)
	if err != nil {
		return err
	}
	defer root.Close()
	return (&Manager{root: root}).VerifyCurrent(ctx, name, hashes...)
}

// VerifyCurrent is a bounded, pinned-root read, not an inventory cache. The
// caller holds the host lock, but systemd and operator changes still exist.
func (m *Manager) VerifyCurrent(ctx context.Context, name string, hashes ...string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !unitNamePattern.MatchString(name) || len(hashes) < 1 || len(hashes) > 2 {
		return errors.New("quadlet: invalid ownership probe")
	}
	for _, hash := range hashes {
		if !validHash(hash) {
			return errors.New("quadlet: invalid ownership probe")
		}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	for _, directory := range []string{ActiveDirectory, stagingDirectory} {
		if err := m.verifyDirectories(directory); err != nil {
			return err
		}
	}
	data, present, err := m.readArtifact(filepath.Join(ActiveDirectory, name))
	if err != nil {
		return err
	}
	if present {
		line, _, _ := bytes.Cut(data, []byte("\n"))
		if !strings.HasPrefix(string(line), marker) || !hashPattern.MatchString(strings.TrimPrefix(string(line), marker)) {
			return refuseOwnership(Unrecorded)
		}
	}
	for _, hash := range hashes {
		if matches(data, present, hash) {
			return ctx.Err()
		}
	}
	return refuseOwnership(HashMismatch)
}
