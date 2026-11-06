//go:build linux

package apply

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"sync"

	"github.com/ShaulLavo/brine/internal/store"
)

var pristineUDPTestSchema = sync.OnceValues(newUDPTestSchema)

func newUDPTestSchema() (data string, err error) {
	directory, err := os.MkdirTemp("", "brine-udp-schema-")
	if err != nil {
		return "", err
	}
	defer func() { err = errors.Join(err, os.RemoveAll(directory)) }()
	db, err := store.Open(directory)
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

func seedSQLiteUDP(directory string) (err error) {
	data, err := pristineUDPTestSchema()
	if err != nil {
		return err
	}
	file, err := os.OpenFile(filepath.Join(directory, "control.db"), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, file.Close()) }()
	_, err = io.WriteString(file, data)
	return err
}
