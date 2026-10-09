//go:build linux

package inventory

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

func openInventoryFile(ctx context.Context, p string, directory bool) (*os.File, error) {
	if e := ctx.Err(); e != nil {
		return nil, e
	}
	path, e := filepath.EvalSymlinks(p)
	if e != nil {
		return nil, e
	}
	info, e := os.Stat(path)
	if e != nil {
		return nil, e
	}
	if (!directory && !info.Mode().IsRegular()) || (directory && !info.IsDir()) {
		return nil, fmt.Errorf("unsupported inventory file type")
	}
	if e := ctx.Err(); e != nil {
		return nil, e
	}
	// The final name may change after Stat. Nonblocking open avoids a FIFO wait;
	// no-follow and a second type check validate the actual opened object.
	fd, e := unix.Open(path, unix.O_RDONLY|unix.O_NONBLOCK|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if e != nil {
		return nil, e
	}
	f := os.NewFile(uintptr(fd), path)
	info, e = f.Stat()
	if e != nil {
		f.Close()
		return nil, e
	}
	if (!directory && !info.Mode().IsRegular()) || (directory && !info.IsDir()) {
		f.Close()
		return nil, fmt.Errorf("unsupported inventory file type")
	}
	if e := ctx.Err(); e != nil {
		f.Close()
		return nil, e
	}
	return f, nil
}
func (HostFS) ReadFile(ctx context.Context, p string) ([]byte, error) {
	return filesystemCall(ctx, func(ctx context.Context) ([]byte, error) {
		f, e := openInventoryFile(ctx, p, false)
		if e != nil {
			return nil, e
		}
		defer f.Close()
		data := make([]byte, fileLimit+1)
		n := 0
		for n < len(data) {
			if e := ctx.Err(); e != nil {
				return nil, e
			}
			m, e := f.Read(data[n:])
			n += m
			if e != nil {
				if errors.Is(e, io.EOF) {
					break
				}
				return nil, e
			}
		}
		if n > fileLimit {
			return nil, fmt.Errorf("file exceeds inventory limit")
		}
		return data[:n], nil
	})
}
func (HostFS) ReadDir(ctx context.Context, p string) ([]fs.DirEntry, error) {
	return filesystemCall(ctx, func(ctx context.Context) ([]fs.DirEntry, error) {
		f, e := openInventoryFile(ctx, p, true)
		if e != nil {
			return nil, e
		}
		defer f.Close()
		entries, e := f.ReadDir(4097)
		if e != nil && !errors.Is(e, io.EOF) {
			return nil, e
		}
		if len(entries) > 4096 {
			return nil, fmt.Errorf("directory exceeds inventory limit")
		}
		return entries, nil
	})
}
func (HostFS) Readlink(ctx context.Context, p string) (string, error) {
	return filesystemCall(ctx, func(ctx context.Context) (string, error) {
		if e := ctx.Err(); e != nil {
			return "", e
		}
		return os.Readlink(p)
	})
}
