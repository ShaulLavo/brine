package cli

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"

	"github.com/ShaulLavo/brine/internal/result"
	"golang.org/x/sys/unix"
)

const initFileName = "brine.toml"

type initDirectory struct {
	root *os.Root
	file *os.File
}

func openInitDirectory(path string) (*initDirectory, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	root, err := os.OpenRoot(absolute)
	if err != nil {
		return nil, err
	}
	file, err := root.Open(".")
	if err != nil {
		return nil, errors.Join(err, root.Close())
	}
	return &initDirectory{root: root, file: file}, nil
}

func (d *initDirectory) Close() error { return errors.Join(d.file.Close(), d.root.Close()) }

// Equality never follows a replaced destination or waits for a special file.
func (d *initDirectory) existing(content []byte) (string, error) {
	before, err := d.root.Lstat(initFileName)
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	if !before.Mode().IsRegular() {
		return "", result.New(result.Conflict, nil)
	}
	file, err := d.root.OpenFile(initFileName, os.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
	if errors.Is(err, unix.ELOOP) || errors.Is(err, os.ErrNotExist) {
		return "", result.New(result.Conflict, err)
	}
	if err != nil {
		return "", err
	}
	defer func() { _ = file.Close() }()
	return d.compare(file, before, content)
}

func (d *initDirectory) compare(file *os.File, before os.FileInfo, content []byte) (string, error) {
	held, err := file.Stat()
	if err != nil {
		return "", err
	}
	if !held.Mode().IsRegular() || !os.SameFile(before, held) {
		return "", result.New(result.Conflict, nil)
	}
	if held.Mode().Perm()&0444 == 0 {
		return "", os.ErrPermission
	}
	raw, err := io.ReadAll(io.LimitReader(file, int64(len(content))+1))
	if err != nil {
		return "", err
	}
	after, err := d.root.Lstat(initFileName)
	if err != nil {
		return "", err
	}
	if !after.Mode().IsRegular() || !os.SameFile(held, after) || after.Size() != int64(len(content)) || !bytes.Equal(raw, content) {
		return "", result.New(result.Conflict, nil)
	}
	return "unchanged", nil
}

// Only publication boundaries are replaceable in file-failure tests.
// Command dependencies never acquire a general filesystem adapter.
type initFileOps struct {
	write         func(*os.File, []byte) error
	sync          func(*os.File) error
	close         func(*os.File) error
	link          func(*os.Root, string, string) error
	syncDirectory func(*os.File) error
}

func defaultInitFileOps() initFileOps {
	return initFileOps{
		write: func(file *os.File, content []byte) error {
			n, err := file.Write(content)
			if err == nil && n != len(content) {
				return io.ErrShortWrite
			}
			return err
		},
		sync:          (*os.File).Sync,
		close:         (*os.File).Close,
		link:          (*os.Root).Link,
		syncDirectory: (*os.File).Sync,
	}
}

func (d *initDirectory) stage() (*os.File, string, error) {
	key, err := randomOperationKey()
	if err != nil {
		return nil, "", err
	}
	name := ".brine-init-" + key + ".tmp"
	file, err := d.root.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	return file, name, err
}

func (d *initDirectory) create(ctx context.Context, content []byte, ops initFileOps) (status string, err error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if status, err := d.existing(content); err != nil || status != "" {
		return status, err
	}
	file, name, err := d.stage()
	if err != nil {
		return "", err
	}
	defer func() { err = errors.Join(err, d.root.Remove(name)) }()
	if err := stageInit(file, content, ops); err != nil {
		return "", err
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if err := ops.link(d.root, name, initFileName); err != nil {
		if errors.Is(err, os.ErrExist) {
			return d.publicationRace(content)
		}
		return "", err
	}
	// Once published, every later failure preserves the created spec.
	return "created", ops.syncDirectory(d.file)
}

func stageInit(file *os.File, content []byte, ops initFileOps) (err error) {
	defer func() { err = errors.Join(err, ops.close(file)) }()
	if err := ops.write(file, content); err != nil {
		return err
	}
	return ops.sync(file)
}

func (d *initDirectory) publicationRace(content []byte) (string, error) {
	status, err := d.existing(content)
	if err != nil || status != "" {
		return status, err
	}
	return "", result.New(result.Conflict, nil)
}
