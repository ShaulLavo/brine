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
	if err != nil || parsed.Zone() != "" {
		return fmt.Errorf("invalid UDP address")
	}
	return nil
}

func (s UDPSocket) validate() error {
	if err := validateUDPAddress(s.Address); err != nil || s.Port < 1024 || s.Port > 65535 {
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
	if err != nil || parsed.Zone() != "" || parsed.IsUnspecified() || parsed.IsMulticast() || parsed.Is4In6() || p.HostPort < 1024 || p.ContainerPort < 1024 || p.NetworkMode == "" || strings.ContainsAny(p.NetworkMode, " \t\r\n") {
		return fmt.Errorf("invalid UDP publication")
	}
	return nil
}
