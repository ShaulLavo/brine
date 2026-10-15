//go:build linux

package host

import (
	"bytes"
	"context"
	"io"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/localexec"
	"github.com/ShaulLavo/brine/internal/store"
	"golang.org/x/sys/unix"
)

func engineBool(value bool) *bool { return &value }

func engineContainerSecurity(container engineContainer, runtime data.EngineRuntime, network string) bool {
	c, h := container.Config, container.HostConfig
	return c.User == "1000:1000" && c.StopSignal == "SIGINT" && c.StopTimeout != nil && *c.StopTimeout == runtime.StopSeconds && c.Umask == "0077" &&
		h.NetworkMode == network && h.ReadonlyRootfs != nil && *h.ReadonlyRootfs && h.Privileged != nil && !*h.Privileged &&
		len(h.CapAdd) == 0 && len(container.BoundingCaps) == 0 && slices.Equal(h.SecurityOpt, []string{"no-new-privileges"}) &&
		h.PidMode == "private" && h.CgroupManager == "systemd" && h.CgroupMode == "private" &&
		h.Memory != nil && *h.Memory == int64(runtime.MemoryMB)<<20 && h.PidsLimit != nil && *h.PidsLimit == int64(runtime.PIDsLimit) &&
		h.NanoCpus != nil && *h.NanoCpus == 1_000_000_000
}

func engineContainerMounts(mounts []engineMount, expected map[string]engineMount) bool {
	if len(mounts) != len(expected) {
		return false
	}
	seen := map[string]bool{}
	for _, mount := range mounts {
		want, ok := expected[mount.Destination]
		if !ok || seen[mount.Destination] || !engineMountPath(mount.Source) || mount.Source != want.Source || mount.Type != want.Type || mount.RW == nil || *mount.RW != *want.RW {
			return false
		}
		seen[mount.Destination] = true
	}
	return true
}

type engineNativeHandle struct {
	pid    uint32
	ticks  uint64
	cgroup string
	fd     int
}

func acquireEngineNativeHandle(container engineContainer, permit store.EnginePermit, material engineMaterial) (*engineNativeHandle, error) {
	if !ownedLiveEngine(container, permit, material) || !*container.State.Running || *container.State.PID <= 0 || !data.ValidCatalogHash(container.ID) {
		return nil, errEngineSource
	}
	scope := "/user.slice/user-" + strconv.Itoa(os.Geteuid()) + ".slice/user@" + strconv.Itoa(os.Geteuid()) + ".service/user.slice/libpod-" + container.ID + ".scope"
	if container.State.CgroupPath != scope {
		return nil, errEngineSource
	}
	process := "/proc/" + strconv.Itoa(*container.State.PID)
	raw, err := engineProcessFile(process, "stat")
	pid, ticks, parseErr := parseEngineProcessStat(string(raw))
	if err != nil || parseErr != nil || uint64(pid) != uint64(*container.State.PID) {
		return nil, errEngineSource
	}
	fd, err := unix.PidfdOpen(int(pid), unix.PIDFD_NONBLOCK)
	if err != nil {
		return nil, errEngineSource
	}
	handle := &engineNativeHandle{pid: pid, ticks: ticks, cgroup: scope + "/container", fd: fd}
	if handle.alive() != nil || !engineNativeOwnership(process, pid) {
		handle.close()
		return nil, errEngineSource
	}
	return handle, nil
}

func bindEngineNative(ctx context.Context, session localexec.Session, container engineContainer, permit store.EnginePermit, material engineMaterial) (*engineNativeHandle, error) {
	handle, err := acquireEngineNativeHandle(container, permit, material)
	if err != nil {
		return nil, err
	}
	process := "/proc/" + strconv.FormatUint(uint64(handle.pid), 10)
	if !engineNativeCommand(process, permit.Runtime) {
		handle.close()
		return nil, errEngineSource
	}
	files, err := openEngineProfileFiles(permit.Reserved.Database.SourcePath(), material, nil)
	if err != nil {
		handle.close()
		return nil, err
	}
	defer files.close()
	if !engineNativeProfileRoot(files, permit) || verifyEngineMountProfileCommand(ctx, session, container, handle, files, engineProfileRunningCommand) != nil || !engineAllocatedSource(permit) {
		handle.close()
		return nil, errEngineSource
	}
	return handle, nil
}

func bindStartingEngineNative(ctx context.Context, session localexec.Session, container engineContainer, permit store.EnginePermit, material engineMaterial) (*engineNativeHandle, error) {
	handle, err := acquireEngineNativeHandle(container, permit, material)
	if err != nil {
		return nil, err
	}
	complete := false
	defer func() {
		if !complete {
			handle.close()
		}
	}()
	files, err := openEngineProfileFiles(permit.Reserved.Database.SourcePath(), material, nil)
	if err != nil {
		return nil, err
	}
	defer files.close()
	process := "/proc/" + strconv.FormatUint(uint64(handle.pid), 10)
	identity, err := engineNativeStartingIdentity(process, handle)
	if err != nil {
		return nil, err
	}
	before, err := files.snapshot()
	if err != nil {
		return nil, err
	}
	guard := func() bool { return engineNativeStartingBound(process, handle, identity, files, before, permit) }
	if !guard() || verifyEngineMountProfileCommand(ctx, session, container, handle, files, engineProfileStartingCommand) != nil || !guard() {
		return nil, errEngineSource
	}
	if waitEngineProcessCommand(ctx, handle, engineNativePath, engineNativeArguments(permit.Runtime, permit.Runtime.HostPort), guard) != nil {
		return nil, errEngineSource
	}
	if verifyEngineMountProfile(ctx, session, container, handle, files) != nil || !guard() || !engineNativeCommand(process, permit.Runtime) || ctx.Err() != nil {
		return nil, errEngineSource
	}
	complete = true
	return handle, nil
}

func engineNativeProfileRoot(files engineProfileFiles, permit store.EnginePermit) bool {
	root, err := files.data.Stat()
	if err != nil || permit.Allocation == nil {
		return false
	}
	stat, ok := root.Sys().(*syscall.Stat_t)
	return ok && uint64(stat.Dev) == permit.Allocation.DirectoryDevice && stat.Ino == permit.Allocation.DirectoryInode
}

func engineNativeStartingIdentity(process string, handle *engineNativeHandle) (engineProfileIdentity, error) {
	user, err := engineProfileNamespaceAt(process, "user")
	if err != nil {
		return engineProfileIdentity{}, err
	}
	mount, err := engineProfileNamespaceAt(process, "mnt")
	if err != nil {
		return engineProfileIdentity{}, err
	}
	return engineProfileIdentity{ticks: handle.ticks, user: user, mount: mount}, nil
}

func engineNativeStartingBound(process string, handle *engineNativeHandle, identity engineProfileIdentity, files engineProfileFiles, before engineMountProfile, permit store.EnginePermit) bool {
	after, err := files.snapshot()
	return err == nil && engineProfileLifetimeBound(process, handle, identity) && engineProfileStable(before, after) && files.helperUnchanged() && engineNativeProfileRoot(files, permit) && engineAllocatedSource(permit)
}

func waitEngineProcessCommand(ctx context.Context, handle *engineNativeHandle, path string, args []string, guard func() bool) error {
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	process := "/proc/" + strconv.FormatUint(uint64(handle.pid), 10)
	for {
		if ctx.Err() != nil || handle.alive() != nil || !guard() {
			return errEngineSource
		}
		if engineProcessCommand(process, path, args) && handle.alive() == nil && guard() && ctx.Err() == nil {
			return nil
		}
		select {
		case <-ctx.Done():
			return errEngineSource
		case <-ticker.C:
		}
	}
}

func engineAllocatedSource(permit store.EnginePermit) bool {
	if permit.Allocation == nil {
		return false
	}
	root, err := engineOpenRoot(permit.Reserved.Database.SourcePath())
	if err != nil {
		return false
	}
	defer func() { _ = root.Close() }()
	file, err := root.Open(".")
	if err != nil {
		return false
	}
	defer func() { _ = file.Close() }()
	member, err := engineMember(file, ".")
	return err == nil && member.Device == permit.Allocation.DirectoryDevice && member.Inode == permit.Allocation.DirectoryInode
}

func (h *engineNativeHandle) close() { _ = unix.Close(h.fd) }

func (h *engineNativeHandle) exited() bool {
	if h.fd < 0 || h.fd > math.MaxInt32 {
		return false
	}
	fds := []unix.PollFd{{Fd: int32(h.fd), Events: unix.POLLIN}}
	n, err := unix.Poll(fds, 0)
	return err == nil && n == 1 && fds[0].Revents&unix.POLLIN != 0
}

func (h *engineNativeHandle) alive() error {
	if h.exited() {
		return errEngineSource
	}
	process := "/proc/" + strconv.FormatUint(uint64(h.pid), 10)
	raw, err := engineProcessFile(process, "stat")
	pid, ticks, parseErr := parseEngineProcessStat(string(raw))
	if err != nil || parseErr != nil || pid != h.pid || ticks != h.ticks {
		return errEngineSource
	}
	raw, err = engineProcessFile(process, "cgroup")
	group, parseErr := parseEngineCgroup(string(raw))
	if err != nil || parseErr != nil || group != h.cgroup {
		return errEngineSource
	}
	return nil
}

func engineNativeCommand(process string, runtime data.EngineRuntime) bool {
	return engineProcessCommand(process, engineNativePath, engineNativeArguments(runtime, runtime.HostPort))
}

func engineProcessCommand(process, path string, args []string) bool {
	exe, err := os.Readlink(filepath.Join(process, "exe"))
	if err != nil || exe != path {
		return false
	}
	raw, err := engineProcessFile(process, "cmdline")
	if err != nil || len(raw) > 8192 || !bytes.HasSuffix(raw, []byte{0}) {
		return false
	}
	arguments := strings.Split(string(raw[:len(raw)-1]), "\x00")
	return slices.Equal(arguments, append([]string{path}, args...))
}

func engineNativeOwnership(process string, pid uint32) bool {
	fields, err := engineProcessStatus(process)
	return err == nil && engineProcessOwned(fields) && slices.Equal(strings.Fields(fields["NSpid"]), []string{strconv.FormatUint(uint64(pid), 10), "1"})
}

func engineProcessStatus(process string) (map[string]string, error) {
	raw, err := engineProcessFile(process, "status")
	if err != nil || len(raw) > 64<<10 {
		return nil, errEngineSource
	}
	fields := map[string]string{}
	for _, line := range strings.Split(string(raw), "\n") {
		key, value, found := strings.Cut(line, ":")
		if found {
			fields[key] = strings.TrimSpace(value)
		}
	}
	return fields, nil
}

func engineProcessFile(process, name string) ([]byte, error) {
	member, valid := strings.CutPrefix(process, "/proc/")
	if !valid || name != "stat" && name != "cgroup" && name != "cmdline" && name != "status" {
		return nil, errEngineSource
	}
	pid, err := strconv.ParseUint(member, 10, 32)
	if member != "self" && (err != nil || pid == 0 || strconv.FormatUint(pid, 10) != member) {
		return nil, errEngineSource
	}
	root, err := os.OpenRoot("/proc")
	if err != nil {
		return nil, errEngineSource
	}
	defer func() { _ = root.Close() }()
	file, err := root.Open(filepath.Join(member, name))
	if err != nil {
		return nil, errEngineSource
	}
	defer func() { _ = file.Close() }()
	raw, err := io.ReadAll(io.LimitReader(file, (64<<10)+1))
	if err != nil || len(raw) > 64<<10 {
		return nil, errEngineSource
	}
	return raw, nil
}

func engineProcessOwned(fields map[string]string) bool {
	uid, gid := strconv.Itoa(os.Geteuid()), strconv.Itoa(os.Getegid())
	return slices.Equal(strings.Fields(fields["Uid"]), []string{uid, uid, uid, uid}) &&
		slices.Equal(strings.Fields(fields["Gid"]), []string{gid, gid, gid, gid}) && fields["NoNewPrivs"] == "1" && fields["CapEff"] == "0000000000000000" && fields["Umask"] == "0077"
}
