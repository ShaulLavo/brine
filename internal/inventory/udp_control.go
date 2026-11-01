package inventory

import (
	"context"
	"net/netip"
	"path/filepath"
	"reflect"
	"strings"

	"github.com/ShaulLavo/brine/internal/target"
)

func (c Collector) bindUDPControl(ctx context.Context, s *target.Snapshot, control *target.ControlInventory, publications map[string]publication, home string) {
	if s.UDPSockets.Value == nil {
		return
	}
	seen := map[target.UDPSocket]bool{}
	for i := range *s.UDPSockets.Value {
		socket := &(*s.UDPSockets.Value)[i]
		if socket.App != "" && !c.udpControlOwns(ctx, *s, control, socket.App, publications[socket.App], home) {
			socket.App = ""
		}
		if seen[*socket] {
			s.UDPSockets = unknown[[]target.UDPSocket]()
			return
		}
		seen[*socket] = true
	}
}

func (c Collector) udpControlOwns(ctx context.Context, s target.Snapshot, control *target.ControlInventory, name string, measured publication, home string) bool {
	if control == nil || s.Apps.Value == nil {
		return false
	}
	var live *target.App
	for i := range *s.Apps.Value {
		if (*s.Apps.Value)[i].Name == name {
			live = &(*s.Apps.Value)[i]
		}
	}
	if live == nil || live.QuadletUnits.Value == nil || live.UnitActive == nil || live.UnitActive.Value == nil || !*live.UnitActive.Value {
		return false
	}
	for _, app := range control.Apps {
		if app.Name == name && app.Status == target.KnownStatus && app.UDP != nil && reflect.DeepEqual(app.UDP.Units, *live.QuadletUnits.Value) && app.UDP.Publication == measured.udpPublication() {
			return c.udpControlMarker(ctx, home, *app.UDP) && udpControlReservation(control.UDPReservations, name, *app.UDP)
		}
	}
	return false
}

func udpControlReservation(rows []target.ControlUDPReservation, name string, head target.ControlUDPRelease) bool {
	if head.Incarnation == "" || head.PlanID == "" {
		return false
	}
	family := "ipv6"
	address, err := netip.ParseAddr(head.Publication.BindAddress)
	if err != nil {
		return false
	}
	if address.Is4() {
		family = "ipv4"
	}
	count := 0
	for _, row := range rows {
		if row.App != name {
			continue
		}
		if row.Family != family || row.State != "active" || row.Owner != "" || row.Incarnation != head.Incarnation || row.BindAddress != head.Publication.BindAddress || row.HostPort != head.Publication.HostPort {
			return false
		}
		count++
	}
	return count == 1
}

func (c Collector) containerAbsent(ctx context.Context, name string) bool {
	out, err := c.probe(ctx, containerOutputLimit, "podman", "--remote=false", "ps", "--all", "--format", containerInventoryFormat)
	if err != nil {
		return false
	}
	for _, line := range strings.Split(out, "\n") {
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		if len(fields) > 2 || !safeToken.MatchString(fields[0]) || len(fields) == 2 && !safeToken.MatchString(fields[1]) {
			return false
		}
		if fields[0] == name || fields[0] == "systemd-"+name || fields[0] == "systemd-brine-"+name {
			return false
		}
		if len(fields) == 2 && (fields[1] == name+".service" || fields[1] == "brine-"+name+".service") {
			return false
		}
	}
	return true
}

func (c Collector) udpControlMarker(ctx context.Context, home string, head target.ControlUDPRelease) bool {
	for _, unit := range head.Units {
		if !strings.HasSuffix(unit.Name, ".container") {
			continue
		}
		raw, err := c.FS.ReadFile(ctx, filepath.Join(home, ".config/containers/systemd", unit.Name))
		return err == nil && digest(raw) == unit.Hash && strings.HasPrefix(string(raw), "# Brine-owned plan="+head.PlanID+"\n")
	}
	return false
}
