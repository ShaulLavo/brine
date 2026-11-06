//go:build linux

package admission

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"
)

// Directory refers to existing admission artifacts. It never initializes missing
// state or creates lock files. Enrollment/bootstrap installation belongs to A2.
// Close it only after its root drain handles are closed. Shared leases own their
// descriptors independently and remain held until Lease.Close.
type Directory struct {
	dir   *os.File
	owner uint32
	// Private syscall seams allow deterministic no-effect and uncertain-write tests.
	syncFile func(*os.File) error
	rename   func(int, string, int, string) error
}

// OpenDirectory requires root-owned, non-writable ancestors, no symlinks at any
// component, and a root-owned 0755 final directory. There is no owner override.
func OpenDirectory(path string) (*Directory, error) {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path || path == "/" {
		return nil, ErrUnavailable
	}
	fd, err := unix.Open("/", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, unavailable(err)
	}
	current := os.NewFile(uintptr(fd), "/")
	for _, component := range strings.Split(strings.TrimPrefix(path, "/"), "/") {
		next, err := openChildDirectory(current, component, 0)
		closeErr := current.Close()
		if err != nil {
			return nil, errors.Join(err, unavailable(closeErr))
		}
		if closeErr != nil {
			return nil, errors.Join(unavailable(closeErr), next.Close())
		}
		current = next
	}
	return checkedDirectory(current, 0)
}

func openChildDirectory(parent *os.File, name string, owner uint32) (*os.File, error) {
	var st unix.Stat_t
	if err := unix.Fstat(int(parent.Fd()), &st); err != nil {
		return nil, unavailable(err)
	}
	if st.Uid != owner || st.Mode&0022 != 0 {
		return nil, ErrUnavailable
	}
	fd, err := unix.Openat(int(parent.Fd()), name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, unavailable(err)
	}
	return os.NewFile(uintptr(fd), name), nil
}

func checkedDirectory(f *os.File, owner uint32) (*Directory, error) {
	var st unix.Stat_t
	err := unix.Fstat(int(f.Fd()), &st)
	if err != nil || st.Uid != owner || st.Mode&unix.S_IFMT != unix.S_IFDIR || st.Mode&07777 != 0755 {
		return nil, errors.Join(ErrUnavailable, err, f.Close())
	}
	return &Directory{dir: f, owner: owner, syncFile: (*os.File).Sync, rename: unix.Renameat}, nil
}

func (d *Directory) Close() error { return d.dir.Close() }

func (d *Directory) Read(ctx context.Context) (_ State, err error) {
	if err = ctx.Err(); err != nil {
		return State{}, err
	}
	f, err := d.openFile("state.json")
	if err != nil {
		return State{}, err
	}
	defer func() { err = errors.Join(err, f.Close()) }()
	raw, err := io.ReadAll(io.LimitReader(f, MaxStateBytes+1))
	if err != nil {
		return State{}, unavailable(err)
	}
	if err = ctx.Err(); err != nil {
		return State{}, err
	}
	return Decode(raw)
}

func (d *Directory) openFile(name string) (*os.File, error) {
	if err := d.checkDirectory(); err != nil {
		return nil, err
	}
	fd, err := unix.Openat(int(d.dir.Fd()), name, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC|unix.O_NONBLOCK, 0)
	if err != nil {
		return nil, unavailable(err)
	}
	f := os.NewFile(uintptr(fd), name)
	if err = d.checkFile(f, name); err != nil {
		return nil, errors.Join(err, f.Close())
	}
	return f, nil
}

func (d *Directory) checkDirectory() error {
	var st unix.Stat_t
	if err := unix.Fstat(int(d.dir.Fd()), &st); err != nil {
		return unavailable(err)
	}
	if st.Uid != d.owner || st.Mode&unix.S_IFMT != unix.S_IFDIR || st.Mode&07777 != 0755 {
		return ErrUnavailable
	}
	return nil
}

func (d *Directory) checkFile(f *os.File, name string) error {
	var st, path unix.Stat_t
	if err := unix.Fstat(int(f.Fd()), &st); err != nil {
		return unavailable(err)
	}
	if st.Uid != d.owner || st.Mode&unix.S_IFMT != unix.S_IFREG || st.Mode&07777 != 0644 || st.Nlink != 1 {
		return ErrUnavailable
	}
	if err := unix.Fstatat(int(d.dir.Fd()), name, &path, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return unavailable(err)
	}
	if st.Dev != path.Dev || st.Ino != path.Ino {
		return ErrUnavailable
	}
	return nil
}

func (d *Directory) save(ctx context.Context, s State) (err error) {
	if err = ctx.Err(); err != nil {
		return err
	}
	if os.Geteuid() != int(d.owner) {
		return ErrUnavailable
	}
	raw, err := Encode(s)
	if err != nil {
		return err
	}
	// Refuse a missing, replaced, symlinked or foreign target even for writes.
	old, err := d.openFile("state.json")
	if err != nil {
		return err
	}
	if err = old.Close(); err != nil {
		return unavailable(err)
	}
	id, err := freshID()
	if err != nil {
		return err
	}
	name := ".state-" + id
	fd, err := unix.Openat(int(d.dir.Fd()), name, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0644)
	if err != nil {
		return unavailable(err)
	}
	f := os.NewFile(uintptr(fd), name)
	defer func() { err = errors.Join(err, removeTemporary(int(d.dir.Fd()), name)) }()
	err = errors.Join(f.Chmod(0644), writeState(d, f, raw))
	if err != nil {
		return unavailable(err)
	}
	if err = ctx.Err(); err != nil {
		return err
	}
	if err = d.rename(int(d.dir.Fd()), name, int(d.dir.Fd()), "state.json"); err != nil {
		return unavailable(err)
	}
	return unavailable(d.syncFile(d.dir))
}

func writeState(d *Directory, f *os.File, raw []byte) error {
	_, err := f.Write(raw)
	if err == nil {
		err = d.syncFile(f)
	}
	return errors.Join(err, f.Close())
}

func removeTemporary(fd int, name string) error {
	err := unix.Unlinkat(fd, name, 0)
	if errors.Is(err, unix.ENOENT) {
		return nil
	}
	return unavailable(err)
}

func unavailable(err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("%w: %w", ErrUnavailable, err)
}
