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

var processPattern = regexp.MustCompile(`\("([^"\n]+)",pid=([0-9]+),fd=([0-9]+)\)`)
var safeToken = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]{0,127}$`)

func (c Collector) listeners(ctx context.Context, s *target.Snapshot, home string) {
	bindings := c.listenerBindings(ctx, s, home)
	ports := map[target.Port]bool{}
	owners := map[target.PortOwner]bool{}
	// The snapshot reserves UDP as well as TCP, even though owners cover TCP only.
	for _, protocol := range []string{"-ltnpe", "-lunp"} {
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
				data, e := c.FS.ReadFile(ctx, "/proc/"+match[2]+"/cgroup")
				if e == nil {
					owner.Unit = unitFromCgroup(string(data))
					cgroup, unified := strings.CutPrefix(strings.TrimSpace(string(data)), "0::")
					if unified && !strings.ContainsRune(cgroup, '\n') && address[:i] == "127.0.0.1" && c.ownsSocket(ctx, match[2], match[3], socketInode(line), string(data)) {
						for path, binding := range bindings {
							if binding.Port == port && (cgroup == path || strings.HasPrefix(cgroup, path+"/")) {
								owner.App = binding.App
							}
						}
					}
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

type listenerBinding struct {
	App  string
	Port target.Port
}

func (c Collector) listenerBindings(ctx context.Context, s *target.Snapshot, home string) map[string]listenerBinding {
	bindings := map[string]listenerBinding{}
	if s.Apps.Status != target.KnownStatus || !c.isRunner(ctx, home) {
		return bindings
	}
	passwd, err := c.FS.ReadFile(ctx, "/etc/passwd")
	if err != nil {
		return bindings
	}
	uid := ""
	for _, line := range strings.Split(string(passwd), "\n") {
		fields := strings.Split(line, ":")
		if len(fields) == 7 && fields[0] == c.RunnerUser && fields[5] == home {
			n, err := strconv.ParseUint(fields[2], 10, 32)
			if err != nil {
				return bindings
			}
			uid = strconv.FormatUint(n, 10)
		}
	}
	if uid == "" {
		return bindings
	}
	root := "/user.slice/user-" + uid + ".slice/user@" + uid + ".service/app.slice/"
	for _, app := range *s.Apps.Value {
		if app.QuadletUnits.Status != target.KnownStatus || app.AllocatedHostPort.Status != target.KnownStatus {
			continue
		}
		var container target.Unit
		count := 0
		for _, unit := range *app.QuadletUnits.Value {
			if strings.HasSuffix(unit.Name, ".container") {
				container = unit
				count++
			}
		}
		if count != 1 {
			continue
		}
		data, err := c.FS.ReadFile(ctx, filepath.Join(home, ".config/containers/systemd", container.Name))
		if err != nil || digest(data) != container.Hash || !renderedUnitMarker.Match(data) {
			continue
		}
		port, ok := pinnedListenerPort(string(data))
		if !ok || port != *app.AllocatedHostPort.Value {
			continue
		}
		path := root + strings.TrimSuffix(container.Name, ".container") + ".service"
		if _, duplicate := bindings[path]; duplicate {
			bindings[path] = listenerBinding{}
			continue
		}
		bindings[path] = listenerBinding{App: app.Name, Port: port}
	}
	return bindings
}

func pinnedListenerPort(data string) (target.Port, bool) {
	section := ""
	value := ""
	count := 0
	for _, line := range strings.Split(data, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "[") {
			section = line
		}
		if section == "[Container]" && strings.HasPrefix(line, "PublishPort=") {
			value = strings.TrimPrefix(line, "PublishPort=")
			count++
		}
	}
	fields := strings.Split(value, ":")
	if count != 1 || len(fields) != 3 || fields[0] != "127.0.0.1" {
		return 0, false
	}
	host, err := strconv.ParseUint(fields[1], 10, 16)
	if err != nil || host < 1024 {
		return 0, false
	}
	inside, err := strconv.ParseUint(fields[2], 10, 16)
	if err != nil || inside < 1024 {
		return 0, false
	}
	return target.Port(host), true
}

func socketInode(line string) string {
	inode := ""
	for _, field := range strings.Fields(line) {
		value, ok := strings.CutPrefix(field, "ino:")
		if !ok {
			continue
		}
		n, err := strconv.ParseUint(value, 10, 64)
		if inode != "" || err != nil || n == 0 {
			return ""
		}
		inode = strconv.FormatUint(n, 10)
	}
	return inode
}

// Numeric PIDs can be reused after ss. Prove this process still owns the same
// socket while its start time and service membership remain stable.
func (c Collector) ownsSocket(ctx context.Context, pid, fd, inode, group string) bool {
	if inode == "" {
		return false
	}
	dir := "/proc/" + pid
	before, err := c.FS.ReadFile(ctx, dir+"/stat")
	if err != nil {
		return false
	}
	start, ok := processStart(string(before), pid)
	if !ok {
		return false
	}
	expected := "socket:[" + inode + "]"
	link, err := c.FS.Readlink(ctx, dir+"/fd/"+fd)
	if err != nil || link != expected {
		return false
	}
	current, err := c.FS.ReadFile(ctx, dir+"/cgroup")
	if err != nil || string(current) != group {
		return false
	}
	link, err = c.FS.Readlink(ctx, dir+"/fd/"+fd)
	if err != nil || link != expected {
		return false
	}
	after, err := c.FS.ReadFile(ctx, dir+"/stat")
	if err != nil {
		return false
	}
	end, ok := processStart(string(after), pid)
	return ok && start == end
}

func processStart(stat, pid string) (uint64, bool) {
	id, _, ok := strings.Cut(stat, " ")
	end := strings.LastIndexByte(stat, ')')
	if !ok || id != pid || end < 0 {
		return 0, false
	}
	fields := strings.Fields(stat[end+1:])
	if len(fields) < 20 {
		return 0, false
	}
	start, err := strconv.ParseUint(fields[19], 10, 64)
	return start, err == nil && start > 0
}
