package inventory

import (
	"context"
	"fmt"
	"net/netip"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/ShaulLavo/brine/internal/target"
)

var processPattern = regexp.MustCompile(`\("([^"\n]+)",pid=([0-9]+),fd=([0-9]+)\)`)
var safeToken = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]{0,127}$`)

type listenerInventory struct {
	ports      map[target.Port]bool
	udpUnknown bool
	udpPorts   []target.Port
	sockets    map[target.UDPSocket]bool
	owners     map[target.PortOwner]bool
}

func (c Collector) listeners(ctx context.Context, s *target.Snapshot, home string, publications map[string]publication) target.Observation[[]target.Port] {
	bindings := c.listenerBindings(ctx, s, home, publications)
	found := listenerInventory{ports: map[target.Port]bool{}, udpPorts: []target.Port{}, sockets: map[target.UDPSocket]bool{}, owners: map[target.PortOwner]bool{}}
	for _, protocol := range []string{"-ltnpe", "-lunpe"} {
		if err := c.collectListenerProtocol(ctx, protocol, bindings, &found); err != nil {
			return unknown[[]target.Port]()
		}
	}
	ports := make([]target.Port, 0, len(found.ports))
	for port := range found.ports {
		ports = append(ports, port)
	}
	slices.Sort(ports)
	sockets := make([]target.UDPSocket, 0, len(found.sockets))
	for socket := range found.sockets {
		sockets = append(sockets, socket)
	}
	owners := make([]target.PortOwner, 0, len(found.owners))
	for owner := range found.owners {
		owners = append(owners, owner)
	}
	sort.Slice(owners, func(i, j int) bool {
		a, b := owners[i], owners[j]
		if a.Port != b.Port {
			return a.Port < b.Port
		}
		if a.Unit != b.Unit {
			return a.Unit < b.Unit
		}
		return a.Process < b.Process
	})
	s.UDPSockets = target.Known(sockets)
	if found.udpUnknown {
		s.UDPSockets = unknown[[]target.UDPSocket]()
	}
	s.UsedPorts = target.Known(ports)
	s.PortOwners = target.Known(owners)
	return target.Known(found.udpPorts)
}

type listenerLine struct {
	Address string
	Port    target.Port
	Text    string
}

func (c Collector) collectListenerProtocol(ctx context.Context, protocol string, bindings map[string]listenerBinding, found *listenerInventory) error {
	out, err := c.probe(ctx, listenerOutputLimit, "ss", "-H", protocol)
	if err != nil {
		return err
	}
	for _, text := range strings.Split(out, "\n") {
		line, err := parseListenerLine(text, protocol)
		if err != nil {
			return err
		}
		if line == nil {
			continue
		}
		if err := c.recordListener(ctx, *line, protocol, bindings, found); err != nil {
			return err
		}
	}
	return nil
}

func parseListenerLine(text, protocol string) (*listenerLine, error) {
	if strings.TrimSpace(text) == "" {
		return nil, nil
	}
	f := strings.Fields(text)
	if len(f) < 5 {
		return nil, fmt.Errorf("invalid listener record")
	}
	state := "LISTEN"
	if protocol == "-lunpe" {
		state = "UNCONN"
	}
	if f[0] == "ESTAB" {
		return nil, nil
	}
	if f[0] != state {
		return nil, fmt.Errorf("unknown listener state")
	}
	if protocol == "-lunpe" && f[4] != "*:*" && f[4] != "0.0.0.0:*" && f[4] != "[::]:*" {
		return nil, nil
	}
	index := strings.LastIndexByte(f[3], ':')
	if index < 0 {
		return nil, fmt.Errorf("invalid listener address")
	}
	address, port := f[3][:index], f[3][index+1:]
	n, err := strconv.ParseUint(port, 10, 16)
	if err != nil || n == 0 {
		return nil, fmt.Errorf("invalid listener port")
	}
	return &listenerLine{Address: address, Port: target.Port(n), Text: text}, nil
}

func (c Collector) recordListener(ctx context.Context, line listenerLine, protocol string, bindings map[string]listenerBinding, found *listenerInventory) error {
	found.ports[line.Port] = true
	if protocol == "-lunpe" {
		found.udpPorts = append(found.udpPorts, line.Port)
		socket, ok := observedUDPSocket(line.Address, line.Port, line.Text)
		if !ok {
			found.udpUnknown = true
			return nil
		}
		socket = c.udpListenerOwner(ctx, line, socket, bindings)
		if found.sockets[socket] {
			found.udpUnknown = true
		}
		found.sockets[socket] = true
		return nil
	}
	matches := processPattern.FindAllStringSubmatch(line.Text, -1)
	if len(matches) == 0 {
		found.owners[target.PortOwner{Port: line.Port}] = true
	}
	for _, match := range matches {
		found.owners[c.tcpListenerOwner(ctx, line, match, bindings)] = true
	}
	return nil
}

func (c Collector) tcpListenerOwner(ctx context.Context, line listenerLine, match []string, bindings map[string]listenerBinding) target.PortOwner {
	owner := target.PortOwner{Port: line.Port}
	if safeToken.MatchString(match[1]) {
		owner.Process = match[1]
	}
	data, err := c.FS.ReadFile(ctx, "/proc/"+match[2]+"/cgroup")
	if err != nil {
		return owner
	}
	owner.Unit = unitFromCgroup(string(data))
	group, unified := strings.CutPrefix(strings.TrimSpace(string(data)), "0::")
	if !unified || strings.ContainsRune(group, '\n') || line.Address != "127.0.0.1" {
		return owner
	}
	for path, binding := range bindings {
		if binding.Port == line.Port && (group == path || strings.HasPrefix(group, path+"/")) && c.ownsSocket(ctx, match[2], match[3], socketInode(line.Text), string(data)) {
			owner.App = binding.App
		}
	}
	return owner
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
	UDP  *target.UDPPublication
}

func (c Collector) listenerBindings(ctx context.Context, s *target.Snapshot, home string, publications map[string]publication) map[string]listenerBinding {
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
		pinned, ok := pinnedListenerPort(string(data))
		measured, observed := publications[app.Name]
		if !ok || !observed || !samePublicationMapping(pinned, measured) || pinned.Host != *app.AllocatedHostPort.Value {
			continue
		}
		path := root + strings.TrimSuffix(container.Name, ".container") + ".service"
		if _, duplicate := bindings[path]; duplicate {
			bindings[path] = listenerBinding{}
			continue
		}
		binding := listenerBinding{App: app.Name, Port: pinned.Host}
		if measured.hasUDP() && measured.NetworkMode == "pasta" {
			udp := measured.udpPublication()
			binding.UDP = &udp
		}
		bindings[path] = binding
	}
	return bindings
}

var tcpPublicationPattern = regexp.MustCompile(`^127\.0\.0\.1:([0-9]+):([0-9]+)$`)
var udpPublicationPattern = regexp.MustCompile(`^(\[[0-9a-f:]+\]|[0-9.]+):([0-9]+):([0-9]+)/udp$`)

func pinnedListenerPort(data string) (publication, bool) {
	section := ""
	var pinned publication
	for _, line := range strings.Split(data, "\n") {
		if strings.HasPrefix(line, "[") {
			section = line
		}
		if section != "[Container]" || !strings.HasPrefix(line, "PublishPort=") {
			continue
		}
		value := strings.TrimPrefix(line, "PublishPort=")
		if !addPinnedPublication(&pinned, value) {
			return publication{}, false
		}
	}
	return pinned, pinned.Host != 0
}

func addPinnedPublication(pinned *publication, value string) bool {
	if fields := tcpPublicationPattern.FindStringSubmatch(value); fields != nil {
		host, validHost := publicationPort(fields[1])
		inside, validInside := publicationPort(fields[2])
		if pinned.Host != 0 || !validHost || !validInside {
			return false
		}
		pinned.Host, pinned.Container = host, inside
		return true
	}
	fields := udpPublicationPattern.FindStringSubmatch(value)
	if fields == nil || pinned.hasUDP() {
		return false
	}
	address := strings.Trim(fields[1], "[]")
	host, validHost := publicationPort(fields[2])
	inside, validInside := publicationPort(fields[3])
	if !validUDPBindAddress(address) || !validHost || !validInside {
		return false
	}
	pinned.UDPAddress, pinned.UDPHost, pinned.UDPContainer = address, host, inside
	return true
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

func observedUDPSocket(endpoint string, port target.Port, line string) (target.UDPSocket, bool) {
	address, device, ok := udpLocalHost(endpoint)
	if !ok {
		return target.UDPSocket{}, false
	}
	socket := target.UDPSocket{Address: address, Interface: device, Port: port}
	matches := processPattern.FindAllStringSubmatch(line, -1)
	if len(matches) == 1 && safeToken.MatchString(matches[0][1]) {
		socket.Process = matches[0][1]
	}
	return socket, true
}

// udpLocalHost parses ss's local host as host[%device], where host is *, an
// IPv4 address or a bracketed IPv6 address. Other spellings are not ss output.
func udpLocalHost(endpoint string) (string, string, bool) {
	host, device, scoped := strings.Cut(endpoint, "%")
	if scoped && !target.ValidInterface(device) {
		return "", "", false
	}
	if host == "*" {
		return host, device, true
	}
	inner, bracketed := strings.CutPrefix(host, "[")
	if bracketed {
		inner, bracketed = strings.CutSuffix(inner, "]")
		if !bracketed {
			return "", "", false
		}
	}
	parsed, err := netip.ParseAddr(inner)
	if err != nil || parsed.Zone() != "" || parsed.Is6() != bracketed {
		return "", "", false
	}
	return parsed.Unmap().String(), device, true
}

func (c Collector) udpListenerOwner(ctx context.Context, line listenerLine, socket target.UDPSocket, bindings map[string]listenerBinding) target.UDPSocket {
	matches := processPattern.FindAllStringSubmatch(line.Text, -1)
	if len(matches) != 1 || socket.Process == "" {
		return socket
	}
	match := matches[0]
	data, err := c.FS.ReadFile(ctx, "/proc/"+match[2]+"/cgroup")
	if err != nil {
		return socket
	}
	socket.Unit = unitFromCgroup(string(data))
	group, unified := strings.CutPrefix(strings.TrimSpace(string(data)), "0::")
	// Owned publications are unscoped pasta binds; a device-bound socket stays foreign.
	if !unified || strings.ContainsRune(group, '\n') || socket.Interface != "" {
		return socket
	}
	for path, binding := range bindings {
		if binding.UDP == nil || socket.Address != binding.UDP.BindAddress || socket.Port != binding.UDP.HostPort {
			continue
		}
		if group != path && !strings.HasPrefix(group, path+"/") {
			continue
		}
		if c.ownsSocket(ctx, match[2], match[3], socketInode(line.Text), string(data)) {
			socket.App = binding.App
		}
	}
	return socket
}
