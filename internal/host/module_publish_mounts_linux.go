//go:build linux

package host

import (
	"io"
	"os"
	"strings"

	"github.com/ShaulLavo/brine/internal/datapublish"
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
		before, _, found := strings.Cut(line, " - ")
		fields := strings.Fields(before)
		if !found || len(fields) < 6 || seen[fields[4]] || !moduleDescriptorMountMode(required, optional, fields[4], fields[5]) {
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
