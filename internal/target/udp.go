package target

import (
	"fmt"
	"net/netip"
	"strings"
)

type UDPSocket struct {
	Address string `json:"address"`
	Port    Port   `json:"port"`
	App     string `json:"app,omitempty"`
	Process string `json:"process,omitempty"`
	Unit    string `json:"unit,omitempty"`
}

type UDPPublication struct {
	BindAddress   string `json:"bind_address"`
	HostPort      Port   `json:"host_port"`
	ContainerPort Port   `json:"container_port"`
	NetworkMode   string `json:"network_mode"`
}

func validateUDPAddress(address string) error {
	if address == "*" || address == "0.0.0.0" || address == "::" {
		return nil
	}
	parsed, err := netip.ParseAddr(address)
	if err != nil || parsed.Zone() != "" || parsed.String() != address || parsed.Is4In6() || parsed.IsMulticast() {
		return fmt.Errorf("invalid UDP address")
	}
	return nil
}

func (s UDPSocket) validate() error {
	if err := validateUDPAddress(s.Address); err != nil || s.Port == 0 || s.Port > 65535 {
		return fmt.Errorf("invalid UDP socket")
	}
	if s.App != "" && checkPattern(appPattern, s.App) != nil {
		return fmt.Errorf("invalid UDP socket app")
	}
	if (s.Process != "" && validToken(s.Process) != nil) || (s.Unit != "" && validToken(s.Unit) != nil) {
		return fmt.Errorf("invalid UDP socket owner")
	}
	return nil
}

func (p UDPPublication) validate() error {
	parsed, err := netip.ParseAddr(p.BindAddress)
	if err != nil || parsed.Zone() != "" || parsed.IsUnspecified() || parsed.IsMulticast() || parsed.Is4In6() || parsed.String() != p.BindAddress || p.HostPort < 1024 || p.HostPort > 65535 || p.ContainerPort < 1024 || p.ContainerPort > 65535 || p.NetworkMode == "" || strings.ContainsAny(p.NetworkMode, " \t\r\n") {
		return fmt.Errorf("invalid UDP publication")
	}
	return nil
}

func validateUDPSockets(sockets []UDPSocket) error {
	if sockets == nil {
		return fmt.Errorf("use [] for known empty UDP sockets")
	}
	seen := map[UDPSocket]bool{}
	for _, socket := range sockets {
		if socket.validate() != nil || seen[socket] {
			return fmt.Errorf("invalid or duplicate UDP socket")
		}
		seen[socket] = true
	}
	return nil
}
func validateLocalAddresses(addresses []string) error {
	if addresses == nil {
		return fmt.Errorf("use [] for known empty local addresses")
	}
	seen := map[string]bool{}
	for _, address := range addresses {
		a, err := netip.ParseAddr(address)
		if err != nil || a.Zone() != "" || a.IsUnspecified() || a.IsMulticast() || a.Is4In6() || a.String() != address || seen[address] {
			return fmt.Errorf("invalid or duplicate local address")
		}
		seen[address] = true
	}
	return nil
}
