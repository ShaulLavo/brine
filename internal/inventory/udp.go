package inventory

import (
	"context"
	"encoding/json"
	"net/netip"
	"slices"

	"github.com/ShaulLavo/brine/internal/target"
)

func (c Collector) localAddresses(ctx context.Context) target.Observation[[]string] {
	raw, err := c.probe(ctx, listenerOutputLimit, "ip", "-j", "addr", "show")
	if err != nil {
		return unknown[[]string]()
	}
	var interfaces []addressInterface
	if json.Unmarshal([]byte(raw), &interfaces) != nil || interfaces == nil {
		return unknown[[]string]()
	}
	addresses := []string{}
	for _, intf := range interfaces {
		if intf.Addresses == nil {
			return unknown[[]string]()
		}
		parsed, ok := localInterfaceAddresses(intf)
		if !ok {
			return unknown[[]string]()
		}
		addresses = append(addresses, parsed...)
	}
	slices.Sort(addresses)
	return target.Known(slices.Compact(addresses))
}

type addressInterface struct {
	Addresses []struct {
		Local string `json:"local"`
	} `json:"addr_info"`
}

func localInterfaceAddresses(intf addressInterface) ([]string, bool) {
	addresses := make([]string, 0, len(intf.Addresses))
	for _, entry := range intf.Addresses {
		addr, err := netip.ParseAddr(entry.Local)
		if err != nil || addr.Zone() != "" {
			return nil, false
		}
		if addr.IsUnspecified() || addr.IsMulticast() || addr.Is4In6() {
			continue
		}
		addresses = append(addresses, addr.String())
	}
	return addresses, true
}
