//go:build linux

package host

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"os"
	"slices"
	"strings"

	"golang.org/x/sys/unix"
)

type descriptorMaskInspection struct {
	Path       string `json:"path"`
	Present    bool   `json:"present"`
	Kind       string `json:"kind"`
	Permission uint32 `json:"permission"`
	Major      uint32 `json:"device_major"`
	Minor      uint32 `json:"device_minor"`
	Mounted    bool   `json:"mounted"`
	ReadOnly   bool   `json:"read_only"`
}

type descriptorProfileInspection struct {
	Purpose         string                     `json:"purpose"`
	Authority       bool                       `json:"authority"`
	Namespace       bool                       `json:"namespace"`
	Network         bool                       `json:"network"`
	ForbiddenAbsent bool                       `json:"forbidden_absent"`
	Process         bool                       `json:"process"`
	MountProfile    bool                       `json:"mount_profile"`
	InputKind       string                     `json:"input_kind"`
	InputHangup     bool                       `json:"input_hangup"`
	MaskInspections []descriptorMaskInspection `json:"mask_inspections"`
}

// RunModuleDescriptorProfile emits bounded fixed-path diagnostics only. It
// reads no Wasm, input bytes, data, key, environment value or arbitrary pathname,
// starts no child, and cannot produce descriptor or publication authority.
func RunModuleDescriptorProfile(ctx context.Context, input io.Reader, output io.Writer) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	inspection := descriptorProfileInspection{Purpose: "module_descriptor_profile_diagnostic", Namespace: os.Getpid() == 1 && os.Geteuid() == 1000 && os.Getegid() == 1000,
		ForbiddenAbsent: true, MaskInspections: []descriptorMaskInspection{}}
	interfaces, err := net.Interfaces()
	inspection.Network = err == nil && len(interfaces) == 1 && interfaces[0].Flags&net.FlagLoopback != 0
	for _, path := range []string{"/data", "/keys", "/home/brine", "/work", "/run/podman/podman.sock", "/var/run/docker.sock"} {
		if _, err := os.Lstat(path); !os.IsNotExist(err) {
			inspection.ForbiddenAbsent = false
		}
	}
	status, err := engineProcessStatus("/proc/self")
	inspection.Process = err == nil && engineProcessOwned(status) && slices.Equal(strings.Fields(status["NSpid"]), []string{"1"})
	raw, err := readModuleGuestMountInfo()
	inspection.MountProfile = err == nil && moduleDescriptorMounts(raw)
	for _, path := range []string{"/proc/kcore", "/proc/acpi", "/proc/scsi", "/proc/sched_debug", "/proc/timer_stats", "/sys/devices/virtual/powercap", "/sys/fs/selinux"} {
		inspection.MaskInspections = append(inspection.MaskInspections, inspectDescriptorMask(path, raw))
	}
	inspection.InputKind, inspection.InputHangup = inspectDescriptorInput(input)
	return json.NewEncoder(output).Encode(inspection)
}

func inspectDescriptorMask(path string, mounts []byte) descriptorMaskInspection {
	inspection := descriptorMaskInspection{Path: path}
	var status unix.Stat_t
	if unix.Lstat(path, &status) != nil {
		return inspection
	}
	inspection.Present, inspection.Permission = true, uint32(status.Mode&07777)
	inspection.Kind = descriptorStatKind(uint32(status.Mode))
	inspection.Major, inspection.Minor = unix.Major(uint64(status.Rdev)), unix.Minor(uint64(status.Rdev))
	for _, line := range strings.Split(strings.TrimSpace(string(mounts)), "\n") {
		before, _, found := strings.Cut(line, " - ")
		fields := strings.Fields(before)
		if !found || len(fields) < 6 || fields[4] != path {
			continue
		}
		inspection.Mounted = true
		inspection.ReadOnly = slices.Contains(strings.Split(fields[5], ","), "ro")
	}
	return inspection
}

func inspectDescriptorInput(input io.Reader) (string, bool) {
	file, ok := input.(*os.File)
	if !ok {
		return "unavailable", false
	}
	var status unix.Stat_t
	if unix.Fstat(int(file.Fd()), &status) != nil {
		return "unavailable", false
	}
	fds := []unix.PollFd{{Fd: int32(file.Fd()), Events: unix.POLLIN | unix.POLLHUP}}
	_, err := unix.Poll(fds, 0)
	return descriptorStatKind(uint32(status.Mode)), err == nil && fds[0].Revents&unix.POLLHUP != 0
}

func descriptorStatKind(mode uint32) string {
	switch mode & unix.S_IFMT {
	case unix.S_IFDIR:
		return "directory"
	case unix.S_IFCHR:
		return "character"
	case unix.S_IFIFO:
		return "pipe"
	case unix.S_IFSOCK:
		return "socket"
	case unix.S_IFREG:
		return "regular"
	default:
		return "other"
	}
}
