package store

import (
	"context"
	"errors"
	"os"
	"path/filepath"
)

// ReadUDPGrantHeld inspects reservations under the operator's mutation fence.
// Missing database bytes prove absence only for an affirmatively initial store.
func ReadUDPGrantHeld(ctx context.Context, stateDir, app string) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if err := readOnlyDirectory(stateDir); err != nil {
		return false, err
	}
	if _, err := os.Lstat(filepath.Join(stateDir, "control.db")); errors.Is(err, os.ErrNotExist) {
		return false, initialUDPGrantState(ctx, stateDir)
	} else if err != nil {
		return false, err
	}
	state, err := OpenSnapshotReadOnly(ctx, stateDir)
	if err != nil {
		return false, err
	}
	held, err := state.UDPGrantHeld(ctx, app)
	return held, errors.Join(err, state.Close())
}

func initialUDPGrantState(ctx context.Context, dir string) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	if len(entries) == 0 {
		return nil
	}
	if len(entries) != 1 || entries[0].Name() != "mutation.lock" {
		return ErrInvalid
	}
	data, err := previewRead(ctx, filepath.Join(dir, "mutation.lock"))
	if err != nil {
		return err
	}
	if len(data) != 0 {
		return ErrInvalid
	}
	return nil
}
