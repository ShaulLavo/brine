package inventory

import (
	"context"
	"fmt"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/ShaulLavo/brine/internal/target"
)

var processPattern = regexp.MustCompile(`\("([^"\n]+)",pid=([0-9]+),fd=[0-9]+\)`)
var safeToken = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]{0,127}$`)

func (c Collector) listeners(ctx context.Context, s *target.Snapshot) {
	ports := map[target.Port]bool{}
	owners := map[target.PortOwner]bool{}
	// The snapshot reserves UDP as well as TCP, even though owners cover TCP only.
	for _, protocol := range []string{"-ltnp", "-lunp"} {
		out, e := c.probe(ctx, "ss", "-H", protocol)
		if e != nil {
			return
		}
		for _, line := range strings.Split(out, "\n") {
			if strings.TrimSpace(line) == "" {
				continue
			}
			f := strings.Fields(line)
			if len(f) < 5 {
				return
			}
			index := 3 // ss normally includes LISTEN/UNCONN state.
			if f[0] != "LISTEN" && f[0] != "UNCONN" {
				index = 2
			}
			if len(f) <= index {
				return
			}
			address := f[index]
			i := strings.LastIndexByte(address, ':')
			if i < 0 {
				return
			}
			n, e := strconv.ParseUint(address[i+1:], 10, 16)
			if e != nil || n == 0 {
				return
			}
			port := target.Port(n)
			ports[port] = true
			if protocol == "-lunp" {
				continue
			}
			matches := processPattern.FindAllStringSubmatch(line, -1)
			if len(matches) == 0 {
				owners[target.PortOwner{Port: port}] = true
				continue
			}
			for _, match := range matches {
				owner := target.PortOwner{Port: port}
				if safeToken.MatchString(match[1]) {
					owner.Process = match[1]
				}
				data, e := c.FS.ReadFile("/proc/" + match[2] + "/cgroup")
				if e == nil {
					owner.Unit = unitFromCgroup(string(data))
				}
				owners[owner] = true
			}
		}
	}
	ps := make([]target.Port, 0, len(ports))
	for p := range ports {
		ps = append(ps, p)
	}
	sort.Slice(ps, func(i, j int) bool { return ps[i] < ps[j] })
	os := make([]target.PortOwner, 0, len(owners))
	for o := range owners {
		os = append(os, o)
	}
	sort.Slice(os, func(i, j int) bool {
		a, b := os[i], os[j]
		if a.Port != b.Port {
			return a.Port < b.Port
		}
		if a.Unit != b.Unit {
			return a.Unit < b.Unit
		}
		return a.Process < b.Process
	})
	s.UsedPorts = target.Known(ps)
	s.PortOwners = target.Known(os)
}

func unitFromCgroup(data string) string {
	for _, line := range strings.Split(data, "\n") {
		f := strings.SplitN(line, ":", 3)
		if len(f) != 3 {
			continue
		}
		components := strings.Split(f[2], "/")
		for i := len(components) - 1; i >= 0; i-- {
			v := components[i]
			if (strings.HasSuffix(v, ".service") || strings.HasSuffix(v, ".scope")) && safeToken.MatchString(v) {
				return v
			}
		}
	}
	return ""
}

func parseGeneration(path string) (uint64, error) {
	base := filepath.Base(filepath.Clean(path))
	if !strings.HasPrefix(base, "gen-") {
		return 0, fmt.Errorf("unrecognized Caddy generation")
	}
	return strconv.ParseUint(strings.TrimPrefix(base, "gen-"), 10, 64)
}
