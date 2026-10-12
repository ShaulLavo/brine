package caddy

import (
	"slices"
	"strings"

	"github.com/ShaulLavo/brine/internal/spec"
)

func verifyManagedSitesLogging(adapted []byte, sites map[string]Site) error {
	domains := []spec.Domain{}
	for _, site := range sites {
		if site.managed != nil {
			domains = append(domains, site.domains...)
		}
	}
	if len(domains) == 0 {
		return nil
	}
	slices.SortFunc(domains, func(a, b spec.Domain) int { return strings.Compare(string(a), string(b)) })
	return VerifyManagedLogging(adapted, slices.Compact(domains))
}
