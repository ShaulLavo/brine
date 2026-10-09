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
	entries, e := c.FS.ReadDir(dir)
	if errors.Is(e, fs.ErrNotExist) {
		s.Apps = target.Known([]target.App{})
		return
	}
	if e != nil {
		return
	}
	byApp := map[string][]target.Unit{}
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasPrefix(name, "brine-") {
			continue
		}
		ext := filepath.Ext(name)
		switch ext {
		case ".container", ".volume", ".network", ".pod", ".kube", ".build", ".image", ".artifact":
		default:
			continue
		}
		app := strings.TrimSuffix(strings.TrimPrefix(name, "brine-"), ext)
		if !appName.MatchString(app) {
			continue
		}
		data, err := c.FS.ReadFile(filepath.Join(dir, name))
		if err != nil {
			return
		}
		byApp[app] = append(byApp[app], target.Unit{Name: name, Hash: digest(data)})
	}
	apps := make([]target.App, 0, len(byApp))
	for app, units := range byApp {
		sort.Slice(units, func(i, j int) bool { return units[i].Name < units[j].Name })
		apps = append(apps, target.App{Name: app, Image: unknown[target.Image](), AllocatedHostPort: unknown[target.Port](), QuadletUnits: target.Known(units), Secrets: unknown[[]target.Secret]()})
	}
	sort.Slice(apps, func(i, j int) bool { return apps[i].Name < apps[j].Name })

	if c.isRunner(home) {
		out, err := c.probe(ctx, "podman", "--remote=false", "secret", "ls", "--format", "{{.ID}} {{.Name}}")
		if err == nil {
			secrets, ok := secretRecords(out)
			if ok {
				for i := range apps {
					observed := []target.Secret{}
					ambiguous := false
					for _, secret := range secrets {
						if !strings.HasPrefix(secret.Name, "brine-"+apps[i].Name+"-") {
							continue
						}
						matches := 0
						for _, app := range apps {
							if strings.HasPrefix(secret.Name, "brine-"+app.Name+"-") {
								matches++
							}
						}
						if matches != 1 {
							ambiguous = true
							continue
						}
						observed = append(observed, secret)
					}
					sort.Slice(observed, func(i, j int) bool { return observed[i].Name < observed[j].Name })
					if !ambiguous {
						apps[i].Secrets = target.Known(observed)
					}
				}
			}
		}
	}

	s.Apps = target.Known(apps)
}

func (c Collector) isRunner(home string) bool {
	passwd, e := c.FS.ReadFile("/etc/passwd")
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
	status, e := c.FS.ReadFile("/proc/self/status")
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
	link, e := c.FS.Readlink(current)
	switch {
	case errors.Is(e, fs.ErrNotExist):
		s.CaddyConfig = absent[target.CaddyConfigSet]()
	case e == nil:
		gen, e := parseGeneration(link)
		if e != nil {
			s.CaddyConfig = target.Observation[target.CaddyConfigSet]{Status: target.Unsupported}
			break
		}
		entries, e := c.FS.ReadDir(current)
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
			data, e := c.FS.ReadFile(filepath.Join(current, entry.Name()))
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

	// Adapt resolves matchers without guessing hosts from Caddyfile text.
	out, e := c.probe(ctx, "caddy", "adapt", "--config", "/etc/caddy/Caddyfile", "--adapter", "caddyfile")
	if e != nil {
		return nil
	}
	if !loopbackAdmin([]byte(out)) {
		return &ConflictError{}
	}
	domains, ok := caddyDomains([]byte(out))
	if !ok {
		return nil
	}
	// Disk configuration is not proof of what survived a reload timeout.
	live, e := c.probe(ctx, "curl", "--disable", "--noproxy", "*", "--silent", "--fail", "--max-time", "2", "http://127.0.0.1:2019/config/")
	if e != nil {
		return nil
	}
	if !loopbackAdmin([]byte(live)) {
		return &ConflictError{}
	}
	liveDomains, ok := caddyDomains([]byte(live))
	if !ok || strings.Join(domains, "\x00") != strings.Join(liveDomains, "\x00") || !sameJSON([]byte(out), []byte(live)) {
		return nil
	}
	paths, ok := c.caddySources("/etc/caddy/Caddyfile")
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
func loopbackAdmin(data []byte) bool {
	var config struct{ Admin struct{ Listen string } }
	if json.Unmarshal(data, &config) != nil {
		return true
	}
	address := config.Admin.Listen
	if address == "" || strings.HasPrefix(address, "unix/") {
		return true
	}
	host, _, e := net.SplitHostPort(address)
	if e != nil {
		return false
	}
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
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
