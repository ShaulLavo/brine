//go:build linux

package host

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/datapublish"
	"github.com/ShaulLavo/brine/internal/localexec"
	"golang.org/x/sys/unix"
)

func startDescriptorProcess(ctx context.Context, lease *descriptorLease, session localexec.Session) (string, *engineNativeHandle, error) {
	created, err := session.Execute(ctx, "/usr/bin/podman", descriptorArguments(lease), nil, true)
	id := strings.TrimSpace(created.Stdout)
	if err != nil || created.Truncated || !data.ValidCatalogHash(id) {
		return "", nil, datapublish.ErrRecord
	}
	if _, err = session.Execute(ctx, "/usr/bin/podman", []string{"start", id}, nil, true); err != nil {
		return id, nil, datapublish.ErrRecord
	}
	containers, err := listEngineContainers(ctx, session)
	if err != nil {
		return id, nil, datapublish.ErrRecord
	}
	for _, container := range containers {
		if container.ID != id {
			continue
		}
		if !ownedDescriptorContainer(lease, container) || descriptorNoSwap(ctx, session, id) != nil {
			return id, nil, datapublish.ErrRecord
		}
		handle, err := bindDescriptorProcess(lease, container)
		return id, handle, err
	}
	return id, nil, datapublish.ErrRecord
}

func bindDescriptorProcess(lease *descriptorLease, container engineContainer) (*engineNativeHandle, error) {
	if !ownedDescriptorContainer(lease, container) || !*container.State.Running || *container.State.PID <= 0 {
		return nil, datapublish.ErrRecord
	}
	uid := strconv.Itoa(os.Geteuid())
	scope := "/user.slice/user-" + uid + ".slice/user@" + uid + ".service/user.slice/libpod-" + container.ID + ".scope"
	if container.State.CgroupPath != scope {
		return nil, datapublish.ErrRecord
	}
	process := "/proc/" + strconv.Itoa(*container.State.PID)
	raw, err := engineProcessFile(process, "stat")
	pid, ticks, parseErr := parseEngineProcessStat(string(raw))
	if err != nil || parseErr != nil || uint64(pid) != uint64(*container.State.PID) {
		return nil, datapublish.ErrRecord
	}
	fd, err := unix.PidfdOpen(int(pid), unix.PIDFD_NONBLOCK)
	if err != nil {
		return nil, datapublish.ErrRecord
	}
	handle := &engineNativeHandle{pid: pid, ticks: ticks, cgroup: scope + "/container", fd: fd}
	if handle.alive() != nil || !engineNativeOwnership(process, pid) || !descriptorProcessBindings(process, lease) {
		handle.close()
		return nil, datapublish.ErrRecord
	}
	return handle, nil
}

func descriptorProcessBindings(process string, lease *descriptorLease) bool {
	raw, err := engineProcessFile(process, "cmdline")
	if err != nil || !bytes.HasSuffix(raw, []byte{0}) || !slices.Equal(strings.Split(string(raw[:len(raw)-1]), "\x00"), []string{"/brine", "host", "module-describe"}) {
		return false
	}
	actual, err := os.Stat(filepath.Join(process, "root", strings.TrimPrefix(moduleDescriptorArtifactPath, "/")))
	if err != nil || !descriptorArtifactIdentity(actual, lease.artifact.member) {
		return false
	}
	helper, err := os.Stat(filepath.Join(process, "exe"))
	protected, protectedErr := os.Stat("/usr/local/bin/brine")
	return err == nil && protectedErr == nil && os.SameFile(helper, protected)
}

func descriptorArtifactIdentity(info os.FileInfo, expected engineTreeMember) bool {
	stat, ok := info.Sys().(*syscall.Stat_t)
	return ok && info.Mode().IsRegular() && uint64(stat.Dev) == expected.Device && stat.Ino == expected.Inode && stat.Uid == expected.UID && stat.Nlink == uint64(expected.Links) && stat.Size == expected.Size
}

func descriptorProcessDisposed(ctx context.Context, handle *engineNativeHandle) error {
	if handle == nil || !handle.exited() || !strings.HasSuffix(handle.cgroup, "/container") {
		return datapublish.ErrRecord
	}
	root, err := os.OpenRoot("/sys/fs/cgroup")
	if err != nil {
		return datapublish.ErrRecord
	}
	defer func() { _ = root.Close() }()
	scope := strings.TrimPrefix(strings.TrimSuffix(handle.cgroup, "/container"), "/")
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	for {
		if _, err = root.Lstat(scope); os.IsNotExist(err) {
			return nil
		}
		if err != nil {
			return datapublish.ErrRecord
		}
		select {
		case <-ctx.Done():
			return datapublish.ErrRecord
		case <-ticker.C:
		}
	}
}
