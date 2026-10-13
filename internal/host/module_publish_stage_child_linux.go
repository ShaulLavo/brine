//go:build linux

package host

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/datapublish"
	"github.com/ShaulLavo/brine/internal/localexec"
	"golang.org/x/sys/unix"
)

func bindModuleStageNativeChild(ctx context.Context, child *localexec.ForegroundProcess, runtime data.EngineRuntime) (*engineNativeHandle, error) {
	if child == nil || child.PID() <= 1 {
		return nil, datapublish.ErrRecord
	}
	process := "/proc/" + strconv.Itoa(child.PID())
	raw, err := engineProcessFile(process, "stat")
	pid, ticks, parseErr := parseEngineProcessStat(string(raw))
	if err != nil || parseErr != nil || strconv.FormatUint(uint64(pid), 10) != strconv.Itoa(child.PID()) {
		return nil, datapublish.ErrRecord
	}
	group, err := moduleStageChildGroup(process)
	if err != nil {
		return nil, err
	}
	fd, err := unix.PidfdOpen(child.PID(), unix.PIDFD_NONBLOCK)
	if err != nil {
		return nil, datapublish.ErrRecord
	}
	handle := &engineNativeHandle{pid: pid, ticks: ticks, cgroup: group, fd: fd}
	if handle.alive() != nil || !moduleStageChildOwned(process, pid) || !moduleStageChildMounts(process) {
		handle.close()
		return nil, datapublish.ErrRecord
	}
	guard := func() bool { return moduleStageChildOwned(process, pid) && moduleStageChildMounts(process) }
	if waitEngineProcessCommand(ctx, handle, engineNativePath, engineNativeArguments(runtime, privateEnginePort), guard) != nil || !moduleStageChildBindings(process, pid, runtime) || handle.alive() != nil {
		handle.close()
		return nil, datapublish.ErrRecord
	}
	return handle, nil
}

func moduleStageChildGroup(process string) (string, error) {
	raw, err := engineProcessFile("/proc/self", "cgroup")
	group, parseErr := parseEngineCgroup(string(raw))
	if err != nil || parseErr != nil {
		return "", datapublish.ErrRecord
	}
	raw, err = engineProcessFile(process, "cgroup")
	actual, parseErr := parseEngineCgroup(string(raw))
	if err != nil || parseErr != nil || actual != group {
		return "", datapublish.ErrRecord
	}
	return group, nil
}

func moduleStageChildBindings(process string, pid uint32, runtime data.EngineRuntime) bool {
	if !moduleStageChildOwned(process, pid) {
		return false
	}
	private := runtime.Clone()
	private.HostPort = privateEnginePort
	return engineNativeCommand(process, private) && moduleStageChildMounts(process)
}

func moduleStageChildOwned(process string, pid uint32) bool {
	fields, err := engineProcessStatus(process)
	return err == nil && engineProcessOwned(fields) && fields["PPid"] == "1" && slices.Equal(strings.Fields(fields["NSpid"]), []string{strconv.FormatUint(uint64(pid), 10)})
}

func moduleStageChildMounts(process string) bool {
	for _, member := range []string{"data", "keys/private.pem", "keys/public.pem"} {
		actual, err := os.Stat(filepath.Join(process, "root", member))
		expected, protectedErr := os.Stat("/" + member)
		if err != nil || protectedErr != nil || !os.SameFile(actual, expected) {
			return false
		}
	}
	return true
}
