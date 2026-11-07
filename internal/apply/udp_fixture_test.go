//go:build linux

package apply

import (
	"context"
	"errors"
	"io"
	"os"
	"sync"

	"github.com/ShaulLavo/brine/internal/store"
)

var pristineUDPSeed struct {
	once sync.Once
	data string
	err  error
}

func pristineUDPTestSchema(ctx context.Context) (string, error) {
	pristineUDPSeed.once.Do(func() { pristineUDPSeed.data, pristineUDPSeed.err = newUDPTestSchema(ctx) })
	return pristineUDPSeed.data, pristineUDPSeed.err
}

func newUDPTestSchema(ctx context.Context) (data string, err error) {
	directory, err := os.MkdirTemp("", "brine-udp-schema-")
	if err != nil {
		return "", err
	}
	defer func() { err = errors.Join(err, os.RemoveAll(directory)) }()
	db, err := store.OpenContext(ctx, directory)
	if err != nil {
		return "", err
	}
	if err := db.Close(); err != nil {
		return "", err
	}
	root, err := os.OpenRoot(directory)
	if err != nil {
		return "", err
	}
	defer func() { err = errors.Join(err, root.Close()) }()
	raw, err := root.ReadFile("control.db")
	return string(raw), err
}

func seedSQLiteUDP(ctx context.Context, directory string) (err error) {
	data, err := pristineUDPTestSchema(ctx)
	if err != nil {
		return err
	}
	root, err := os.OpenRoot(directory)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, root.Close()) }()
	file, err := root.OpenFile("control.db", os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, file.Close()) }()
	_, err = io.WriteString(file, data)
	return err
}
