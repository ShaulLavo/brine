package inventory

import (
	"context"
	"strings"

	"github.com/ShaulLavo/brine/internal/target"
)

const containerInventoryFormat = `{{.Names}} {{.Label "PODMAN_SYSTEMD_UNIT"}}`

func (c Collector) absence(ctx context.Context, s *target.Snapshot, artifacts appArtifacts, control *target.ControlInventory, udpPorts target.Observation[[]target.Port]) {
	if udpPorts.Status != target.KnownStatus || !artifacts.runner || s.Apps.Status != target.KnownStatus || s.Generation.Status != target.KnownStatus || s.UsedPorts.Status != target.KnownStatus || s.PortOwners.Status != target.KnownStatus {
		return
	}
	if control == nil && *s.Generation.Value != 0 {
		return
	}
	states := map[string]target.ControlApp{}
	if control != nil {
		for _, app := range control.Apps {
			states[app.Name] = app
		}
	}
	candidates := []int{}
	for i, app := range *s.Apps.Value {
		state, recorded := states[app.Name]
		if app.QuadletUnits.Status == target.KnownStatus && len(*app.QuadletUnits.Value) == 0 && (!recorded || state.Status == target.Absent && control.Target != nil) {
			candidates = append(candidates, i)
		}
	}
	if len(candidates) == 0 {
		return
	}
	out, err := c.probe(ctx, "podman", "--remote=false", "ps", "--all", "--format", containerInventoryFormat)
	if err != nil {
		return
	}
	containers := map[string]bool{}
	units := map[string]bool{}
	for _, line := range strings.Split(out, "\n") {
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		if len(fields) > 2 || !safeToken.MatchString(fields[0]) || len(fields) == 2 && !safeToken.MatchString(fields[1]) {
			return
		}
		containers[fields[0]] = true
		if len(fields) == 2 {
			units[fields[1]] = true
		}
	}
	for _, i := range candidates {
		app := &(*s.Apps.Value)[i]
		if containers[app.Name] || containers["systemd-"+app.Name] || containers["systemd-brine-"+app.Name] || units[app.Name+".service"] || units["brine-"+app.Name+".service"] {
			continue
		}
		state := states[app.Name]
		state.Name = app.Name
		if !listenersAbsent(*s, state, *udpPorts.Value) {
			continue
		}
		app.Image = absent[target.Image]()
		app.AllocatedHostPort = absent[target.Port]()
	}
}

func listenersAbsent(s target.Snapshot, app target.ControlApp, udpPorts []target.Port) bool {
	for _, owner := range *s.PortOwners.Value {
		if owner.App == app.Name || app.Name != "" && (owner.Unit == app.Name+".service" || owner.Unit == "brine-"+app.Name+".service") {
			return false
		}
	}
	for _, port := range app.RetiredPorts {
		for _, udpPort := range udpPorts {
			if udpPort == port {
				return false
			}
		}
		for _, used := range *s.UsedPorts.Value {
			if used != port {
				continue
			}
			owned := false
			for _, owner := range *s.PortOwners.Value {
				if owner.Port == port {
					if owner.App == "" || owner.App == app.Name {
						return false
					}
					owned = true
				}
			}
			if !owned {
				return false
			}
		}
	}
	return true
}
