//go:build linux

package enroll

import (
	"path/filepath"
	"strings"
)

func proofMounts(raw string) bool {
	if raw == "" || !strings.HasSuffix(raw, "\n") {
		return false
	}
	for _, line := range strings.Split(strings.TrimSuffix(raw, "\n"), "\n") {
		if !proofMountLine(line) {
			return false
		}
	}
	return true
}

func proofMountLine(line string) bool {
	parts := strings.Split(line, " - ")
	if len(parts) != 2 || len(strings.Fields(parts[1])) != 3 {
		return false
	}
	fields := strings.Fields(parts[0])
	if len(fields) < 6 || !proofMountNumbers(fields) {
		return false
	}
	root, err := proofMountPath(fields[3])
	if err != nil || !filepath.IsAbs(root) {
		return false
	}
	mount, err := proofMountPath(fields[4])
	if err != nil || !filepath.IsAbs(mount) || filepath.Clean(mount) != mount {
		return false
	}
	return !proofWithin(mount, home) && !proofWithin(mount, "/etc/caddy/brine")
}

func proofMountNumbers(fields []string) bool {
	id, err := proofNumber(fields[0])
	if err != nil || id == 0 {
		return false
	}
	if _, err := proofNumber(fields[1]); err != nil {
		return false
	}
	device := strings.Split(fields[2], ":")
	if len(device) != 2 {
		return false
	}
	for _, part := range device {
		if _, err := proofNumber(part); err != nil {
			return false
		}
	}
	return true
}

func proofMountPath(raw string) (string, error) {
	var out strings.Builder
	for i := 0; i < len(raw); i++ {
		if raw[i] != '\\' {
			out.WriteByte(raw[i])
			continue
		}
		if i+4 > len(raw) {
			return "", errAdmissionObservation
		}
		escape := raw[i : i+4]
		decoded, ok := map[string]byte{`\040`: ' ', `\011`: '\t', `\012`: '\n', `\134`: '\\'}[escape]
		if !ok {
			return "", errAdmissionObservation
		}
		out.WriteByte(decoded)
		i += 3
	}
	return out.String(), nil
}

func proofWithin(path, root string) bool {
	return path == root || strings.HasPrefix(path, root+"/")
}
