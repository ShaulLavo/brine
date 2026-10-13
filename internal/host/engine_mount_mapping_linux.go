//go:build linux

package host

import (
	"golang.org/x/sys/unix"
	"io"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
)

type engineIDRange struct{ inside, outside, count uint64 }
type engineIDMap struct {
	ranges   []engineIDRange
	overflow uint32
}
type engineProfileMappings struct{ uid, gid engineIDMap }

func parseEngineIDMap(raw []byte, overflow uint32) (engineIDMap, error) {
	result := engineIDMap{overflow: overflow}
	if len(raw) == 0 || len(raw) > 4096 {
		return result, errEngineSource
	}
	lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
	if len(lines) > 128 {
		return result, errEngineSource
	}
	for _, line := range lines {
		row, err := parseEngineIDRange(line)
		if err != nil {
			return result, err
		}
		if overlapsEngineIDRanges(result.ranges, row) {
			return result, errEngineSource
		}
		result.ranges = append(result.ranges, row)
	}
	return result, nil
}
func parseEngineIDRange(line string) (engineIDRange, error) {
	fields := strings.Fields(line)
	if len(fields) != 3 {
		return engineIDRange{}, errEngineSource
	}
	values := make([]uint64, 3)
	for i, field := range fields {
		value, err := strconv.ParseUint(field, 10, 32)
		if err != nil {
			return engineIDRange{}, errEngineSource
		}
		values[i] = value
	}
	row := engineIDRange{inside: values[0], outside: values[1], count: values[2]}
	if row.count == 0 || row.inside+row.count > uint64(math.MaxUint32)+1 || row.outside+row.count > uint64(math.MaxUint32)+1 {
		return row, errEngineSource
	}
	return row, nil
}
func overlapsEngineIDRanges(rows []engineIDRange, next engineIDRange) bool {
	for _, row := range rows {
		if next.inside < row.inside+row.count && row.inside < next.inside+next.count {
			return true
		}
		if next.outside < row.outside+row.count && row.outside < next.outside+next.count {
			return true
		}
	}
	return false
}
func (m engineIDMap) namespaceID(host uint32) uint32 {
	for _, row := range m.ranges {
		if uint64(host) >= row.outside && uint64(host)-row.outside < row.count {
			return uint32(row.inside + uint64(host) - row.outside) //nolint:gosec // Parsed ranges fit uint32 and host is within this range.
		}
	}
	return m.overflow
}
func (m engineProfileMappings) equal(next engineProfileMappings) bool {
	return m.uid.overflow == next.uid.overflow && m.gid.overflow == next.gid.overflow && slices.Equal(m.uid.ranges, next.uid.ranges) && slices.Equal(m.gid.ranges, next.gid.ranges)
}
func readEngineMappingFile(path string) ([]byte, error) {
	file, err := os.Open(path) //nolint:gosec // Only fixed kernel overflow paths and the bound PID's UID/GID maps reach this reader.
	if err != nil {
		return nil, errEngineSource
	}
	defer func() { _ = file.Close() }()
	raw, err := io.ReadAll(io.LimitReader(file, 4097))
	if err != nil || len(raw) == 0 || len(raw) > 4096 {
		return nil, errEngineSource
	}
	return raw, nil
}
func readEngineProfileMappings(process string) (engineProfileMappings, error) {
	result := engineProfileMappings{}
	for name, target := range map[string]*engineIDMap{"uid": &result.uid, "gid": &result.gid} {
		overflowRaw, err := readEngineMappingFile("/proc/sys/kernel/overflow" + name)
		if err != nil {
			return result, err
		}
		overflow, err := strconv.ParseUint(strings.TrimSpace(string(overflowRaw)), 10, 32)
		if err != nil {
			return result, errEngineSource
		}
		raw, err := readEngineMappingFile(filepath.Join(process, name+"_map"))
		if err != nil {
			return result, err
		}
		*target, err = parseEngineIDMap(raw, uint32(overflow))
		if err != nil {
			return result, err
		}
	}
	return result, nil
}

func engineProfileNodeMatches(host, guest *engineProfileNode, m engineProfileMappings) bool {
	if host == nil || guest == nil {
		return host == nil && guest == nil
	}
	if host.Device != guest.Device || host.Inode != guest.Inode || host.Mode != guest.Mode || guest.UID != m.uid.namespaceID(host.UID) || guest.GID != m.gid.namespaceID(host.GID) || guest.Size < 0 {
		return false
	}
	switch host.Mode & unix.S_IFMT {
	case unix.S_IFDIR:
		return host.Mode&07777 == 0700
	case unix.S_IFREG:
		return host.Size == guest.Size && host.Links == guest.Links
	default:
		return false
	}
}
func engineProfileMatches(host, guest engineMountProfile, m engineProfileMappings) bool {
	return engineProfileNodeMatches(host.Helper, guest.Helper, m) && engineProfileNodeMatches(host.Data, guest.Data, m) && engineProfileNodeMatches(host.PrivateKey, guest.PrivateKey, m) && engineProfileNodeMatches(host.PublicKey, guest.PublicKey, m) && engineProfileNodeMatches(host.Artifact, guest.Artifact, m)
}
func engineProfileStable(first, second engineMountProfile) bool {
	identity := engineProfileMappings{uid: engineIDMap{ranges: []engineIDRange{{count: uint64(math.MaxUint32) + 1}}}, gid: engineIDMap{ranges: []engineIDRange{{count: uint64(math.MaxUint32) + 1}}}}
	return engineProfileMatches(first, second, identity)
}
