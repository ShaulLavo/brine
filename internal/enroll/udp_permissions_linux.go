//go:build linux

package enroll

import (
	"context"
	"errors"
	"os"
	"syscall"
	"time"

	"github.com/ShaulLavo/brine/internal/localexec"
	"github.com/ShaulLavo/brine/internal/strictjson"
	"golang.org/x/sys/unix"
)

func operatorUDPGrantHeld(ctx context.Context, app string) (bool, error) {
	return udpGrantHeldVia(ctx, localexec.ExecRunner{}, app)
}

func udpGrantHeldVia(ctx context.Context, runner localexec.CaptureRunner, app string) (bool, error) {
	if !targetName.MatchString(app) {
		return false, errPermissionsRefused
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	out, err := runner.CaptureStdout(ctx, 4096, enrollmentToolPath("runuser"), "-u", "brine", "--", binaryPath, "host", "udp-grant-check", app)
	if err != nil || out.Overflow {
		return false, errPermissionsRefused
	}
	fields, err := strictjson.Object([]byte(out.Stdout), "held")
	if err != nil {
		return false, errPermissionsRefused
	}
	return strictjson.Value[bool](fields["held"])
}

func lockUDPGrantState(ctx context.Context, path string, uid, gid int) (*os.File, error) {
	fd, err := unix.Openat2(unix.AT_FDCWD, path, &unix.OpenHow{Flags: unix.O_RDONLY | unix.O_CLOEXEC | unix.O_NOFOLLOW | unix.O_NONBLOCK, Resolve: unix.RESOLVE_NO_SYMLINKS})
	if errors.Is(err, unix.ENOENT) {
		fd, err = createUDPGrantLock(path, uid, gid)
	}
	if err != nil {
		return nil, err
	}
	f := os.NewFile(uintptr(fd), path)
	var stat unix.Stat_t
	if err = unix.Fstat(fd, &stat); err != nil || int(stat.Uid) != uid || stat.Mode&unix.S_IFMT != unix.S_IFREG || stat.Mode&0077 != 0 {
		_ = f.Close()
		return nil, errPermissionsRefused
	}
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		err = unix.Flock(fd, unix.LOCK_EX|unix.LOCK_NB)
		if err == nil {
			return f, nil
		}
		if !errors.Is(err, syscall.EWOULDBLOCK) && !errors.Is(err, syscall.EINTR) {
			_ = f.Close()
			return nil, err
		}
		select {
		case <-ctx.Done():
			_ = f.Close()
			return nil, ctx.Err()
		case <-ticker.C:
		}
	}
}

func createUDPGrantLock(path string, uid, gid int) (int, error) {
	fd, err := unix.Openat2(unix.AT_FDCWD, path, &unix.OpenHow{Flags: unix.O_RDWR | unix.O_CREAT | unix.O_EXCL | unix.O_CLOEXEC, Mode: 0600, Resolve: unix.RESOLVE_NO_SYMLINKS})
	if err != nil {
		return -1, err
	}
	if err = unix.Fchown(fd, uid, gid); err != nil {
		_ = unix.Close(fd)
		return -1, err
	}
	return fd, nil
}
