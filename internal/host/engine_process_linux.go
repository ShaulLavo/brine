//go:build linux

package host

import (
	"os"
	"path"
	"strconv"
	"strings"

	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/systemd"
)

func engineStarterProcess() (systemd.StarterProcess, error) {
	stat, err := os.ReadFile("/proc/self/stat")
	if err != nil {
		return systemd.StarterProcess{}, errEngineSource
	}
	pid, ticks, err := parseEngineProcessStat(string(stat))
	if err != nil || uint64(os.Getpid()) != uint64(pid) {
		return systemd.StarterProcess{}, errEngineSource
	}
	cgroup, err := os.ReadFile("/proc/self/cgroup")
	if err != nil {
		return systemd.StarterProcess{}, errEngineSource
	}
	group, err := parseEngineCgroup(string(cgroup))
	invocation := os.Getenv("INVOCATION_ID")
	if err != nil || !data.ValidID(invocation) {
		return systemd.StarterProcess{}, errEngineSource
	}
	return systemd.StarterProcess{PID: pid, StartTicks: ticks, InvocationID: invocation, ControlGroup: group}, nil
}

func parseEngineProcessStat(raw string) (uint32, uint64, error) {
	start := strings.IndexByte(raw, '(')
	end := strings.LastIndexByte(raw, ')')
	if start < 1 || end <= start {
		return 0, 0, errEngineSource
	}
	pid, err := strconv.ParseUint(strings.TrimSpace(raw[:start]), 10, 32)
	fields := strings.Fields(raw[end+1:])
	if err != nil || pid == 0 || len(fields) < 20 {
		return 0, 0, errEngineSource
	}
	ticks, err := strconv.ParseUint(fields[19], 10, 64)
	if err != nil || ticks == 0 {
		return 0, 0, errEngineSource
	}
	return uint32(pid), ticks, nil
}

func parseEngineCgroup(raw string) (string, error) {
	lines := strings.Split(strings.TrimSpace(raw), "\n")
	if len(lines) != 1 {
		return "", errEngineSource
	}
	group, ok := strings.CutPrefix(lines[0], "0::")
	if !ok || len(group) > 4096 || !path.IsAbs(group) || path.Clean(group) != group || strings.ContainsAny(group, "\x00\r\n") {
		return "", errEngineSource
	}
	return group, nil
}
