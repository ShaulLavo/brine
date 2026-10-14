//go:build linux

package host

import (
	"errors"
	"io"
	"os"
	"slices"
	"strings"

	"github.com/ShaulLavo/brine/internal/datapublish"
	"golang.org/x/sys/unix"
)

func readModuleGuestMountInfo() ([]byte, error) {
	file, err := os.Open("/proc/self/mountinfo")
	if err != nil {
		return nil, datapublish.ErrRecord
	}
	defer func() { _ = file.Close() }()
	raw, err := io.ReadAll(io.LimitReader(file, (64<<10)+1))
	if err != nil || len(raw) == 0 || len(raw) > 64<<10 {
		return nil, datapublish.ErrRecord
	}
	return raw, nil
}

func moduleDescriptorMounts(raw []byte) bool {
	if len(raw) == 0 || len(raw) > 64<<10 {
		return false
	}
	required := map[string]string{"/": "ro", moduleDescriptorArtifactPath: "ro", "/brine": "ro", "/proc": "rw", "/sys": "ro", "/sys/fs/cgroup": "ro", "/tmp": "rw"}
	optional := map[string]bool{}
	for _, path := range []string{"/dev", "/dev/pts", "/dev/shm", "/dev/mqueue", "/dev/null", "/dev/zero", "/dev/full", "/dev/tty", "/dev/random", "/dev/urandom", "/run", "/var/tmp", "/run/.containerenv", "/etc/hostname", "/etc/hosts", "/etc/resolv.conf", "/proc/keys", "/proc/latency_stats", "/proc/timer_list", "/proc/asound", "/proc/bus", "/proc/fs", "/proc/irq", "/proc/sys", "/proc/sysrq-trigger", "/sys/firmware"} {
		optional[path] = true
	}
	seen := map[string]bool{}
	lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
	if len(lines) > 256 {
		return false
	}
	for _, line := range lines {
		before, after, found := strings.Cut(line, " - ")
		fields := strings.Fields(before)
		if !found || len(fields) < 6 || seen[fields[4]] || !moduleDescriptorMountMode(required, optional, fields[4], fields[5]) && !moduleReadonlyRuntimeMask(fields, strings.Fields(after)) {
			return false
		}
		seen[fields[4]] = true
	}
	for path := range required {
		if !seen[path] {
			return false
		}
	}
	return true
}

func moduleDescriptorMountMode(required map[string]string, optional map[string]bool, path, flags string) bool {
	mode, mandatory := required[path]
	if !mandatory {
		return optional[path]
	}
	readOnly, writable := false, false
	for _, flag := range strings.Split(flags, ",") {
		readOnly = readOnly || flag == "ro"
		writable = writable || flag == "rw"
	}
	return mode == "ro" && readOnly && !writable || mode == "rw" && writable && !readOnly
}

type moduleRuntimeMaskKind uint8

const (
	moduleRuntimeMaskNone moduleRuntimeMaskKind = iota
	moduleRuntimeMaskEmpty
	moduleRuntimeMaskNull
)

func moduleRuntimeMaskShape(fields, filesystem []string) moduleRuntimeMaskKind {
	if len(fields) < 6 || len(filesystem) < 3 {
		return moduleRuntimeMaskNone
	}
	flags := strings.Split(fields[5], ",")
	if !slices.Contains(flags, "ro") || slices.Contains(flags, "rw") {
		return moduleRuntimeMaskNone
	}
	if fields[4] == "/proc/acpi" && fields[3] == "/" && filesystem[0] == "tmpfs" && filesystem[1] == "tmpfs" {
		return moduleRuntimeMaskEmpty
	}
	if fields[4] == "/proc/kcore" && fields[3] == "/null" && filesystem[0] == "devtmpfs" && filesystem[1] == "udev" && slices.Contains(flags, "nosuid") {
		return moduleRuntimeMaskNull
	}
	return moduleRuntimeMaskNone
}

func moduleReadonlyRuntimeMask(fields, filesystem []string) bool {
	kind := moduleRuntimeMaskShape(fields, filesystem)
	if kind == moduleRuntimeMaskNone {
		return false
	}
	var status unix.Stat_t
	if unix.Lstat(fields[4], &status) != nil || !moduleRuntimeMaskStat(kind, status) {
		return false
	}
	if kind == moduleRuntimeMaskNull {
		return true
	}
	directory, err := os.Open("/proc/acpi")
	if err != nil {
		return false
	}
	defer func() { _ = directory.Close() }()
	return moduleRuntimeMaskDirectoryEmpty(directory)
}

func moduleRuntimeMaskDirectoryEmpty(directory *os.File) bool {
	entries, err := directory.ReadDir(1)
	return len(entries) == 0 && errors.Is(err, io.EOF)
}

func moduleRuntimeMaskStat(kind moduleRuntimeMaskKind, status unix.Stat_t) bool {
	if kind == moduleRuntimeMaskEmpty {
		return status.Mode == unix.S_IFDIR|01777 && status.Uid == 0 && status.Gid == 0 && status.Nlink == 2
	}
	return kind == moduleRuntimeMaskNull && status.Mode == unix.S_IFCHR|0666 && status.Uid == 65534 && status.Gid == 65534 && status.Nlink == 1 && status.Size == 0 && unix.Major(uint64(status.Rdev)) == 1 && unix.Minor(uint64(status.Rdev)) == 3
}
