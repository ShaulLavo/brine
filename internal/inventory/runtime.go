package inventory

import (
	"context"
	"encoding/json"
	"net/netip"
	"regexp"
	"strconv"
	"strings"

	"github.com/ShaulLavo/brine/internal/target"
)

var renderedUnitMarker = regexp.MustCompile(`^# Brine-owned plan=sha256:[0-9a-f]{64}\n`)

const runtimePortFormat = `{"name":{{json .Name}},"running":{{json .State.Running}},"unit":{{json (index .Config.Labels "PODMAN_SYSTEMD_UNIT")}},"network":{{json .HostConfig.NetworkMode}},"ports":{{json .NetworkSettings.Ports}}}`

type publication struct {
	Host         target.Port
	Container    target.Port
	UDPAddress   string
	UDPHost      target.Port
	UDPContainer target.Port
	NetworkMode  string
}

func (p publication) hasUDP() bool { return p.UDPAddress != "" }

func samePublicationMapping(a, b publication) bool {
	return a.Host == b.Host && a.Container == b.Container && a.UDPAddress == b.UDPAddress && a.UDPHost == b.UDPHost && a.UDPContainer == b.UDPContainer
}

// Runtime observations never grant replacement authority. Only committed state does.
func (c Collector) livePublication(ctx context.Context, units []target.Unit) target.Observation[publication] {
	unknownPort := unknown[publication]()
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
	out, err := c.probe(ctx, runtimeOutputLimit, "podman", "--remote=false", "inspect", "--type", "container", "--format", runtimePortFormat, "systemd-"+container)
	if err != nil {
		return unknownPort
	}
	var runtime struct {
		Name    string                      `json:"name"`
		Running bool                        `json:"running"`
		Unit    string                      `json:"unit"`
		Network string                      `json:"network"`
		Ports   map[string][]runtimeBinding `json:"ports"`
	}
	if json.Unmarshal([]byte(out), &runtime) != nil || !runtime.Running || runtime.Name != "systemd-"+container || runtime.Unit != container+".service" {
		return unknownPort
	}
	var published publication
	for protocol, bindings := range runtime.Ports {
		if !published.addRuntimeBinding(protocol, bindings, runtime.Network) {
			return unknownPort
		}
	}
	if published.Host == 0 {
		return unknownPort
	}
	published.NetworkMode = runtime.Network
	return target.Known(published)
}

func validUDPBindAddress(value string) bool {
	address, err := netip.ParseAddr(value)
	return err == nil && address.Zone() == "" && !address.IsUnspecified() && !address.IsMulticast() && !address.Is4In6() && address.String() == value
}

func publicationPort(value string) (target.Port, bool) {
	n, err := strconv.ParseUint(value, 10, 16)
	return target.Port(n), err == nil && n >= 1024 && strconv.FormatUint(n, 10) == value
}

type runtimeBinding struct {
	HostIP   string `json:"HostIp"`
	HostPort string `json:"HostPort"`
}

func (p *publication) addRuntimeBinding(protocol string, bindings []runtimeBinding, network string) bool {
	// Unbound image EXPOSE entries allocate no host port.
	if len(bindings) == 0 {
		return true
	}
	if len(bindings) != 1 {
		return false
	}
	portText, tcp := strings.CutSuffix(protocol, "/tcp")
	if !tcp {
		portText = strings.TrimSuffix(protocol, "/udp")
	}
	inside, validInside := publicationPort(portText)
	host, validHost := publicationPort(bindings[0].HostPort)
	if !validInside || !validHost {
		return false
	}
	if tcp {
		if p.Host != 0 || bindings[0].HostIP != "127.0.0.1" {
			return false
		}
		p.Host, p.Container = host, inside
		return true
	}
	if !strings.HasSuffix(protocol, "/udp") || p.hasUDP() || network != "pasta" || !validUDPBindAddress(bindings[0].HostIP) {
		return false
	}
	p.UDPAddress, p.UDPHost, p.UDPContainer = bindings[0].HostIP, host, inside
	return true
}
