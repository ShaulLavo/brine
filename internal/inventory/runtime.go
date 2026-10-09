package inventory

import (
	"context"
	"encoding/json"
	"regexp"
	"strconv"
	"strings"

	"github.com/ShaulLavo/brine/internal/target"
)

var renderedUnitMarker = regexp.MustCompile(`^# Brine-owned plan=sha256:[0-9a-f]{64}\n`)

const runtimePortFormat = `{"name":{{json .Name}},"running":{{json .State.Running}},"unit":{{json (index .Config.Labels "PODMAN_SYSTEMD_UNIT")}},"ports":{{json .NetworkSettings.Ports}}}`

// Runtime observations never grant replacement authority. Only committed state does.
func (c Collector) livePort(ctx context.Context, units []target.Unit) target.Observation[target.Port] {
	unknownPort := unknown[target.Port]()
	container := ""
	for _, u := range units {
		if strings.HasSuffix(u.Name, ".container") {
			if container != "" {
				return unknownPort
			}
			container = strings.TrimSuffix(u.Name, ".container")
		}
	}
	if container == "" {
		return unknownPort
	}
	out, err := c.probe(ctx, "podman", "--remote=false", "inspect", "--type", "container", "--format", runtimePortFormat, "systemd-"+container)
	if err != nil {
		return unknownPort
	}
	var runtime struct {
		Name    string `json:"name"`
		Running bool   `json:"running"`
		Unit    string `json:"unit"`
		Ports   map[string][]struct {
			HostIP   string `json:"HostIp"`
			HostPort string `json:"HostPort"`
		} `json:"ports"`
	}
	if json.Unmarshal([]byte(out), &runtime) != nil || !runtime.Running || runtime.Name != "systemd-"+container || runtime.Unit != container+".service" || len(runtime.Ports) != 1 {
		return unknownPort
	}
	for protocol, bindings := range runtime.Ports {
		portText, ok := strings.CutSuffix(protocol, "/tcp")
		inside, e := strconv.ParseUint(portText, 10, 16)
		if !ok || e != nil || inside < 1024 || len(bindings) != 1 || bindings[0].HostIP != "127.0.0.1" {
			return unknownPort
		}
		port, e := strconv.ParseUint(bindings[0].HostPort, 10, 16)
		if e != nil || port < 1024 {
			return unknownPort
		}
		return target.Known(target.Port(port))
	}
	return unknownPort
}
