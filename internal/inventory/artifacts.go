package inventory

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"net"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strings"

	"github.com/ShaulLavo/brine/internal/target"
)

var appName = regexp.MustCompile(`^[a-z][a-z0-9-]{0,62}$`)

func (c Collector) apps(ctx context.Context, s *target.Snapshot, home string, exists bool) {
	if s.Runner.User.Status == target.Unknown {
		return
	}
	if !exists {
		s.Apps = target.Known([]target.App{})
		return
	}
	dir := filepath.Join(home, ".config/containers/systemd")
	entries, e := c.FS.ReadDir(ctx, dir)
	if e != nil && !errors.Is(e, fs.ErrNotExist) {
		return
	}
	if errors.Is(e, fs.ErrNotExist) {
		entries = []fs.DirEntry{}
	}
	byApp := map[string][]target.Unit{}
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() {
			continue
		}
		ext := filepath.Ext(name)
		switch ext {
		case ".container", ".volume", ".network", ".pod", ".kube", ".build", ".image", ".artifact":
		default:
			continue
		}
		data, err := c.FS.ReadFile(ctx, filepath.Join(dir, name))
		if err != nil {
			return
		}
		app := strings.TrimSuffix(strings.TrimPrefix(name, "brine-"), ext)
		if renderedUnitMarker.Match(data) {
			app = strings.TrimSuffix(name, ext)
		} else if !strings.HasPrefix(name, "brine-") {
			continue
		}
		if !appName.MatchString(app) {
			continue
		}
		byApp[app] = append(byApp[app], target.Unit{Name: name, Hash: digest(data)})
	}

	secrets := []target.Secret{}
	runnerIdentity := c.isRunner(ctx, home)
	if runnerIdentity {
		out, err := c.probe(ctx, "podman", "--remote=false", "secret", "ls", "--format", "{{.ID}} {{.Name}}")
		if err != nil {
			return
		}
		var ok bool
		secrets, ok = secretRecords(out)
		if !ok {
			return
		}
		for _, secret := range secrets {
			if !strings.HasPrefix(secret.Name, "brine-") {
				continue
			}
			candidates := secretAppNames(secret.Name)
			// Names are not self-delimiting. Do not guess app/reference boundaries.
			if len(candidates) != 1 {
				return
			}
			if _, ok := byApp[candidates[0]]; !ok {
				byApp[candidates[0]] = []target.Unit{}
			}
		}
	} else if len(byApp) == 0 {
		return
	}
	apps := make([]target.App, 0, len(byApp))
	for app, units := range byApp {
		sort.Slice(units, func(i, j int) bool { return units[i].Name < units[j].Name })
		a := target.App{Name: app, Image: unknown[target.Image](), AllocatedHostPort: unknown[target.Port](), QuadletUnits: target.Known(units), Secrets: unknown[[]target.Secret]()}
		// Empty artifacts on a measured fresh control state have no Brine allocation
		// or deployed image. Existing/unknown state never receives invented absence.
		if len(units) == 0 && s.Generation.Value != nil && *s.Generation.Value == 0 {
			a.Image = absent[target.Image]()
			a.AllocatedHostPort = absent[target.Port]()
		}
		if runnerIdentity {
			if len(units) > 0 {
				a.AllocatedHostPort = c.livePort(ctx, units)
			}
			observed := []target.Secret{}
			for _, secret := range secrets {
				if strings.HasPrefix(secret.Name, "brine-"+app+"-") {
					observed = append(observed, secret)
				}
			}
			sort.Slice(observed, func(i, j int) bool { return observed[i].Name < observed[j].Name })
			a.Secrets = target.Known(observed)
		}
		apps = append(apps, a)
	}
	sort.Slice(apps, func(i, j int) bool { return apps[i].Name < apps[j].Name })

	s.Apps = target.Known(apps)
}

func (c Collector) isRunner(ctx context.Context, home string) bool {
	passwd, e := c.FS.ReadFile(ctx, "/etc/passwd")
	if e != nil {
		return false
	}
	uid := ""
	for _, line := range strings.Split(string(passwd), "\n") {
		f := strings.Split(line, ":")
		if len(f) == 7 && f[0] == c.RunnerUser && f[5] == home {
			uid = f[2]
		}
	}
	status, e := c.FS.ReadFile(ctx, "/proc/self/status")
	if e != nil || uid == "" {
		return false
	}
	for _, line := range strings.Split(string(status), "\n") {
		if strings.HasPrefix(line, "Uid:") {
			f := strings.Fields(line)
			return len(f) == 5 && f[1] == uid && f[2] == uid
		}
	}
	return false
}

func (c Collector) caddy(ctx context.Context, s *target.Snapshot) error {
	const current = "/etc/caddy/brine/current"
	link, e := c.FS.Readlink(ctx, current)
	switch {
	case errors.Is(e, fs.ErrNotExist):
		s.CaddyConfig = absent[target.CaddyConfigSet]()
	case e == nil:
		gen, e := parseGeneration(link)
		if e != nil {
			s.CaddyConfig = target.Observation[target.CaddyConfigSet]{Status: target.Unsupported}
			break
		}
		entries, e := c.FS.ReadDir(ctx, current)
		if e != nil {
			break
		}
		files := []target.CaddyFile{}
		complete := true
		for _, entry := range entries {
			if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".caddy") {
				continue
			}

			if !regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]{0,127}\.caddy$`).MatchString(entry.Name()) {
				complete = false
				break
			}
			data, e := c.FS.ReadFile(ctx, filepath.Join(current, entry.Name()))
			if e != nil {
				complete = false
				break
			}
			files = append(files, target.CaddyFile{Name: entry.Name(), Hash: digest(data)})
		}
		if complete {
			sort.Slice(files, func(i, j int) bool { return files[i].Name < files[j].Name })
			s.CaddyConfig = target.Known(target.CaddyConfigSet{Generation: gen, Files: files})
		}
	}

	// Check the active endpoint even if adaptation of disk configuration fails.
	live, liveErr := c.probe(ctx, "curl", "--disable", "--noproxy", "*", "--silent", "--fail", "--max-time", "2", "http://127.0.0.1:2019/config/")
	liveAdmin := unknown[bool]()
	if liveErr == nil {
		liveAdmin = adminBinding([]byte(live))
		if liveAdmin.Value != nil && !*liveAdmin.Value {
			return &ConflictError{}
		}
	}
	// Adapt resolves matchers without guessing hosts from Caddyfile text.
	out, adaptErr := c.probe(ctx, "caddy", "adapt", "--config", "/etc/caddy/Caddyfile", "--adapter", "caddyfile")
	if adaptErr != nil {
		return nil
	}
	diskAdmin := adminBinding([]byte(out))
	if diskAdmin.Value != nil && !*diskAdmin.Value {
		return &ConflictError{}
	}
	if liveErr != nil || liveAdmin.Status != target.KnownStatus || diskAdmin.Status != target.KnownStatus {
		return nil
	}
	domains, ok := caddyDomains([]byte(out))
	if !ok {
		return nil
	}
	liveDomains, ok := caddyDomains([]byte(live))
	if !ok || strings.Join(domains, "\x00") != strings.Join(liveDomains, "\x00") || !sameJSON([]byte(out), []byte(live)) {
		return nil
	}
	paths, ok := c.caddySources(ctx, "/etc/caddy/Caddyfile")
	if !ok {
		return nil
	}
	files := make([]target.LiveCaddyFile, 0, len(paths))
	for _, path := range paths {
		file := target.LiveCaddyFile{Name: "file-" + strings.TrimPrefix(digest([]byte(path)), "sha256:"), Domains: unknown[[]string]()}
		if len(paths) == 1 {
			file.Domains = target.Known(liveDomains)
		} else if path != "/etc/caddy/Caddyfile" {
			adapted, err := c.probe(ctx, "caddy", "adapt", "--config", path, "--adapter", "caddyfile")
			if err == nil {
				if d, ok := caddyDomains([]byte(adapted)); ok {
					file.Domains = target.Known(d)
				}
			}
		}
		// Root attribution stays unknown with imports because adapt erases provenance.
		files = append(files, file)
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Name < files[j].Name })
	s.LiveCaddyFiles = target.Known(files)
	return nil

}

func caddyDomains(data []byte) ([]string, bool) {
	var root struct {
		Apps struct {
			HTTP struct {
				Servers map[string]struct{ Routes []json.RawMessage }
			}
		}
	}
	if json.Unmarshal(data, &root) != nil {
		return nil, false
	}
	hosts := map[string]bool{}
	valid := true
	var route func(json.RawMessage)
	route = func(raw json.RawMessage) {
		var r struct {
			Match []map[string]json.RawMessage
		}
		if json.Unmarshal(raw, &r) != nil {
			valid = false
			return
		}
		if len(r.Match) == 0 {
			hosts["*"] = true
		}
		for _, m := range r.Match {
			v, ok := m["host"]
			if !ok {
				hosts["*"] = true
				continue
			}
			var names []string
			if json.Unmarshal(v, &names) != nil || len(names) == 0 {
				valid = false
				continue
			}
			for _, name := range names {
				d, e := target.CanonicalDomain(name)
				if e != nil {
					valid = false
				} else {
					hosts[d] = true
				}
			}
		}

	}
	for _, server := range root.Apps.HTTP.Servers {
		for _, raw := range server.Routes {
			route(raw)
		}
	}
	domains := make([]string, 0, len(hosts))
	for d := range hosts {
		domains = append(domains, d)
	}
	sort.Strings(domains)
	return domains, valid
}

// ConflictError refuses an observed Caddy administration listener off loopback.
type ConflictError struct{}

func (*ConflictError) Error() string { return "Caddy administration must be restricted to loopback" }
func (*ConflictError) Code() string  { return "conflict" }

func adminBinding(data []byte) target.Observation[bool] {
	var object map[string]json.RawMessage
	if json.Unmarshal(data, &object) != nil || object == nil {
		return unknown[bool]()
	}
	var config struct {
		Admin struct {
			Listen   string
			Disabled bool
		}
	}
	if json.Unmarshal(data, &config) != nil {
		return unknown[bool]()
	}
	if config.Admin.Disabled {
		return target.Known(true)
	}
	address := config.Admin.Listen
	if address == "" || strings.HasPrefix(address, "unix/") {
		return target.Known(true)
	}
	host, _, e := net.SplitHostPort(address)
	if e != nil {
		return unknown[bool]()
	}
	if host == "localhost" {
		return target.Known(true)
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return target.Known(false)
	}
	return target.Known(ip.IsLoopback())
}

func secretRecords(out string) ([]target.Secret, bool) {
	secrets := []target.Secret{}
	names := map[string]bool{}
	ids := map[string]bool{}
	for _, line := range strings.Split(out, "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		f := strings.Fields(line)
		if len(f) != 2 || !safeToken.MatchString(f[0]) || !safeToken.MatchString(f[1]) || ids[f[0]] || names[f[1]] {
			return nil, false
		}
		ids[f[0]] = true
		names[f[1]] = true
		secrets = append(secrets, target.Secret{ID: f[0], Name: f[1]})
	}
	return secrets, true
}

func sameJSON(a, b []byte) bool {
	var left, right any
	return json.Unmarshal(a, &left) == nil && json.Unmarshal(b, &right) == nil && reflect.DeepEqual(left, right)
}

var secretReferenceName = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]{0,252}$`)
var secretVersionSuffix = regexp.MustCompile(`-v[0-9]+$`)

func secretAppNames(name string) []string {
	stem := strings.TrimPrefix(name, "brine-")
	stem = secretVersionSuffix.ReplaceAllString(stem, "")
	names := []string{}
	for i, ch := range stem {
		if ch == '-' && appName.MatchString(stem[:i]) && secretReferenceName.MatchString(stem[i+1:]) {
			names = append(names, stem[:i])
		}
	}
	return names
}
