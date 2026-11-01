package policy

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"github.com/ShaulLavo/brine/internal/strictjson"
	"net/netip"
	"regexp"
	"strings"

	"github.com/ShaulLavo/brine/internal/spec"
)

type UDPGrant struct {
	App           string           `toml:"app" json:"app"`
	Exposure      spec.UDPExposure `toml:"exposure" json:"exposure"`
	BindAddress   string           `toml:"bind_address" json:"bind_address"`
	ClientAddress string           `toml:"client_address" json:"client_address"`
	HostPorts     PortRange        `toml:"host_ports" json:"host_ports"`
}

var udpAppPattern = regexp.MustCompile(`^[a-z][a-z0-9-]{0,62}$`)
var udpDNSPattern = regexp.MustCompile(`^(?:[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?\.)+[a-z](?:[a-z0-9-]{0,61}[a-z0-9])?$`)

func (g UDPGrant) CanonicalBytes() ([]byte, error) { return json.Marshal(g) }

func (g UDPGrant) Hash() string {
	b, _ := g.CanonicalBytes()
	sum := sha256.Sum256(b)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func (g UDPGrant) Validate(appPorts PortRange) error {
	if !udpAppPattern.MatchString(g.App) {
		return fmt.Errorf("invalid app")
	}
	if g.Exposure != spec.UDPPublic && g.Exposure != spec.UDPTailnet {
		return fmt.Errorf("invalid exposure")
	}
	bind, err := canonicalBindAddress(g.BindAddress)
	if err != nil || bind != g.BindAddress {
		return fmt.Errorf("invalid bind address")
	}
	if g.ClientAddress == "" || canonicalClientAddress(g.ClientAddress) != g.ClientAddress {
		return fmt.Errorf("invalid client address")
	}
	if g.HostPorts.Min < 1024 || g.HostPorts.Max < g.HostPorts.Min || uint32(g.HostPorts.Max)-uint32(g.HostPorts.Min) > 63 {
		return fmt.Errorf("invalid host port range")
	}
	if rangesOverlap(g.HostPorts, appPorts) || containsFixedPort(g.HostPorts) {
		return fmt.Errorf("UDP range overlaps reserved HTTP ports")
	}
	return nil
}

func canonicalBindAddress(raw string) (string, error) {
	addr, err := netip.ParseAddr(raw)
	if err != nil || addr.Zone() != "" || addr.IsUnspecified() || addr.IsMulticast() || addr.Is4In6() {
		return "", fmt.Errorf("invalid address")
	}
	return addr.String(), nil
}

func canonicalClientAddress(raw string) string {
	if addr, err := netip.ParseAddr(raw); err == nil {
		if addr.Zone() != "" || addr.Is4In6() {
			return ""
		}
		return addr.String()
	}
	if udpDNSPattern.MatchString(strings.ToLower(raw)) && len(raw) <= 253 && !strings.HasSuffix(raw, ".") {
		return strings.ToLower(raw)
	}
	return ""
}

func rangesOverlap(a, b PortRange) bool { return a.Min <= b.Max && b.Min <= a.Max }

func containsFixedPort(r PortRange) bool {
	for _, port := range []spec.Port{80, 443, 2019} {
		if r.Min <= port && port <= r.Max {
			return true
		}
	}
	return false
}

func bindFamily(address string) string {
	if parsed, err := netip.ParseAddr(address); err == nil && parsed.Is4() {
		return "ipv4"
	}
	return "ipv6"
}

func (g UDPGrant) BindFamily() string { return bindFamily(g.BindAddress) }

// ParseUDPGrant accepts only the reviewed operator document, never runtime options.
func ParseUDPGrant(raw []byte) (UDPGrant, error) {
	if len(raw) > 4096 {
		return UDPGrant{}, strictjson.ErrObject
	}
	fields, err := strictjson.Object(raw, "app", "exposure", "bind_address", "client_address", "host_ports")
	if err != nil {
		return UDPGrant{}, err
	}
	ports, err := strictjson.Object(fields["host_ports"], "min", "max")
	if err != nil {
		return UDPGrant{}, err
	}
	for _, value := range fields {
		if string(value) == "null" {
			return UDPGrant{}, strictjson.ErrObject
		}
	}
	var g UDPGrant
	if err = json.Unmarshal(raw, &g); err != nil {
		return UDPGrant{}, strictjson.ErrObject
	}
	if _, err = strictjson.Value[spec.Port](ports["min"]); err != nil {
		return UDPGrant{}, err
	}
	if _, err = strictjson.Value[spec.Port](ports["max"]); err != nil {
		return UDPGrant{}, err
	}
	if err = g.Validate(PortRange{}); err != nil {
		return UDPGrant{}, err
	}
	return g, nil
}

func udpGrantsOverlap(grant UDPGrant, prior []UDPGrant) bool {
	for _, g := range prior {
		if grant.BindAddress == g.BindAddress && rangesOverlap(grant.HostPorts, g.HostPorts) {
			return true
		}
	}
	return false
}
func validUDPPolicyKeys(value any) bool {
	rows, ok := value.([]any)
	if !ok {
		return false
	}
	for _, row := range rows {
		m, ok := row.(map[string]any)
		if !ok || !onlyKeys(m, "app", "exposure", "bind_address", "client_address", "host_ports") {
			return false
		}
		ports, ok := m["host_ports"].(map[string]any)
		if !ok || !onlyKeys(ports, "min", "max") {
			return false
		}
	}
	return true
}
