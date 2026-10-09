package caddy

import (
	"errors"
	"fmt"
	"strings"

	"github.com/ShaulLavo/brine/internal/policy"
	"github.com/ShaulLavo/brine/internal/spec"
)

// Site can only be constructed through the spec and operator-policy boundary.
// Its fields are private because spec's branded primitives permit forged casts.
type Site struct {
	name    spec.Name
	domains []spec.Domain
	port    spec.Port
}

func NewSite(app spec.App, p policy.Policy, hostPort spec.Port) (Site, error) {
	d, err := policy.Normalize(app, p)
	if err != nil {
		return Site{}, err
	}
	if hostPort < d.AppPorts.Min || hostPort > d.AppPorts.Max {
		return Site{}, errors.New("caddy: host port outside operator policy")
	}
	return Site{name: d.Name, domains: d.Domains, port: hostPort}, nil
}

func Render(site Site) ([]byte, error) {
	if site.name == "" {
		return nil, errors.New("caddy: site required")
	}
	domains := make([]string, len(site.domains))
	for i, domain := range site.domains {
		domains[i] = string(domain)
	}
	return fmt.Appendf(nil, "%s {\n\treverse_proxy 127.0.0.1:%d\n\theader X-Content-Type-Options nosniff\n\theader Referrer-Policy no-referrer\n}\n", strings.Join(domains, ", "), site.port), nil
}
