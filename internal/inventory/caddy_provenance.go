package inventory

import (
	"bytes"
	"context"
	"encoding/json"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/ShaulLavo/brine/internal/caddy"
	"github.com/ShaulLavo/brine/internal/policy"
	"github.com/ShaulLavo/brine/internal/spec"
	"github.com/ShaulLavo/brine/internal/target"
)

// Route occurrences, not domain sets, retain foreign owners that serve the same
// host. Any import whose standalone routes cannot be found stays unattributed.
type routeConfig struct {
	Apps struct {
		HTTP struct {
			Servers map[string]*struct {
				Routes []json.RawMessage `json:"routes"`
			} `json:"servers"`
		} `json:"http"`
	} `json:"apps"`
}

func (c Collector) caddyProvenance(ctx context.Context, s *target.Snapshot, whole []byte, paths []string) {
	var remaining routeConfig
	if json.Unmarshal(whole, &remaining) != nil {
		return
	}
	files := make([]target.LiveCaddyFile, 0, len(paths))
	for _, path := range paths {
		if path == "/etc/caddy/Caddyfile" {
			continue
		}
		file := target.LiveCaddyFile{Name: "file-" + strings.TrimPrefix(digest([]byte(path)), "sha256:"), Domains: unknown[[]string]()}
		adapted, err := c.probe(ctx, "caddy", "adapt", "--config", path, "--adapter", "caddyfile")
		if err != nil {
			return
		}
		domains, ok := caddyDomains([]byte(adapted))
		if !ok {
			return
		}
		var imported routeConfig
		if json.Unmarshal([]byte(adapted), &imported) != nil {
			return
		}
		for _, server := range imported.Apps.HTTP.Servers {
			for _, route := range server.Routes {
				found := false
				for _, liveServer := range remaining.Apps.HTTP.Servers {
					for i, candidate := range liveServer.Routes {
						if sameJSON(route, candidate) {
							liveServer.Routes = append(liveServer.Routes[:i], liveServer.Routes[i+1:]...)
							found = true
							break
						}
					}
					if found {
						break
					}
				}
				if !found {
					return
				}
			}
		}
		file.Domains = target.Known(domains)
		c.associateCaddy(ctx, s, path, &file)
		files = append(files, file)
	}
	residual, err := json.Marshal(remaining)
	if err != nil {
		return
	}
	domains, ok := caddyDomains(residual)
	if !ok {
		return
	}
	files = append(files, target.LiveCaddyFile{Name: "file-" + strings.TrimPrefix(digest([]byte("/etc/caddy/Caddyfile")), "sha256:"), Domains: target.Known(domains)})
	s.LiveCaddyFiles = target.Known(files)
}

func (c Collector) associateCaddy(ctx context.Context, s *target.Snapshot, path string, file *target.LiveCaddyFile) {
	if filepath.Dir(path) != "/etc/caddy/brine/current" || s.CaddyConfig.Value == nil {
		return
	}
	name := filepath.Base(path)
	app := strings.TrimSuffix(name, ".caddy")
	if !appName.MatchString(app) {
		return
	}
	data, err := c.FS.ReadFile(ctx, path)
	if err != nil {
		return
	}
	bound := false
	for _, observed := range s.CaddyConfig.Value.Files {
		if observed.Name == name && observed.Hash == digest(data) {
			bound = true
			break
		}
	}
	if !bound {
		return
	}
	const prefix = "\treverse_proxy 127.0.0.1:"
	lines := strings.Split(string(data), "\n")
	if len(lines) < 2 || !strings.HasPrefix(lines[1], prefix) {
		return
	}
	port, err := strconv.ParseUint(strings.TrimPrefix(lines[1], prefix), 10, 16)
	if err != nil {
		return
	}
	domains := make([]spec.Domain, len(*file.Domains.Value))
	for i, domain := range *file.Domains.Value {
		domains[i] = spec.Domain(domain)
	}
	site, err := caddy.CommittedSite(policy.Desired{Name: spec.Name(app), Domains: domains}, spec.Port(port))
	if err != nil {
		return
	}
	rendered, err := caddy.Render(site)
	if err != nil || !bytes.Equal(rendered, data) {
		return
	}
	file.Name = name
	file.App = app
}
