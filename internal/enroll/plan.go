// Package enroll implements the operator-only enrollment boundary.
package enroll

import (
	"bufio"
	"errors"
	"io"
	"regexp"
	"strings"

	"github.com/ShaulLavo/brine/internal/target"
)

type Package struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

type Facts struct {
	SSHAuthorizationChecked bool              `json:"ssh_authorization_checked"`
	PackageInstall          []Package         `json:"package_install"`
	Snapshot                target.Snapshot   `json:"snapshot"`
	HostKey                 string            `json:"host_key"`
	Packages                map[string]string `json:"packages"`
	PermitUserEnvironment   string            `json:"permit_user_environment"`
	PAMChecked              bool              `json:"pam_checked"`
	PAMUserEnvironment      bool              `json:"pam_user_environment"`
	OwnedRunner             bool              `json:"owned_runner"`
}
type Plan struct {
	Changes         []string `json:"changes"`
	MissingPackages []string `json:"missing_packages"`
}

func MakePlan(f Facts) (Plan, error) {
	fail := func() (Plan, error) {
		return Plan{}, errors.New("enrollment refused: unsupported, conflicting, or incomplete host inventory")
	}
	s := f.Snapshot
	if s.OS.ID != "debian" || s.OS.Version != "13" || (s.Arch != "amd64" && s.Arch != "arm64") || s.CgroupV2.Value == nil || !*s.CgroupV2.Value || s.Versions.Systemd.Value == nil || !regexp.MustCompile(`^257(?:[.-]|$)`).MatchString(*s.Versions.Systemd.Value) {
		return fail()
	}
	for _, version := range []struct {
		name    string
		value   *string
		pattern string
	}{{"podman", s.Versions.Podman.Value, `^5\.4(?:\.|$)`}, {"caddy", s.Versions.Caddy.Value, `^v?2\.6(?:\.|$)`}, {"passt", s.Versions.Passt.Value, `^0\.0~git20250503\.`}} {
		if f.Packages[version.name] != "" && (version.value == nil || !regexp.MustCompile(version.pattern).MatchString(*version.value)) {
			return fail()
		}
	}
	for name, pattern := range map[string]string{"podman": `^(?:[0-9]+:)?5\.4(?:\.|-|$)`, "caddy": `^(?:[0-9]+:)?2\.6(?:\.|-|$)`, "passt": `^(?:[0-9]+:)?0\.0~git20250503\.`} {
		count := 0
		for _, pkg := range f.PackageInstall {
			if strings.Split(pkg.Name, ":")[0] == name {
				count++
				if !regexp.MustCompile(pattern).MatchString(pkg.Version) {
					return fail()
				}
			}
		}
		if count > 1 || (f.Packages[name] == "" && count != 1) {
			return fail()
		}
	}
	if s.Runner.User.Status != target.Absent && !f.OwnedRunner {
		return fail()
	}
	if !f.SSHAuthorizationChecked || s.UsedPorts.Value == nil || s.PortOwners.Value == nil || !f.PAMChecked || f.PAMUserEnvironment || f.PermitUserEnvironment != "no" {
		return fail()
	}
	for _, p := range *s.UsedPorts.Value {
		if p == 80 || p == 443 {
			found := false
			for _, o := range *s.PortOwners.Value {
				if o.Port == p && o.Process == "caddy" {
					found = true
				}
				if o.Port == p && o.Process != "caddy" {
					return fail()
				}
			}
			if !found {
				return fail()
			}
		}
	}
	for _, o := range *s.PortOwners.Value {
		if (o.Port == 80 || o.Port == 443) && o.Process != "caddy" {
			return fail()
		}
	}
	p := Plan{Changes: []string{
		"Install missing Debian podman, passt and caddy packages and their declared dependencies; never upgrade or remove existing packages during enrollment.",
		"Before any package installation, mask Caddy so its package default site cannot start; its post-install normally enables and starts caddy.service.",
		"Podman's netavark dependency can enable netavark-dhcp-proxy.service, netavark-dhcp-proxy.socket and netavark-firewalld-reload.service, and activate the DHCP proxy socket on installation. These package post-install effects are part of the transaction.",
		"Create only the Brine runner account, with an empty skeleton and login shell /bin/sh; refuse a preexisting unowned account, group or home.",
		"Create a root-owned home (root:brine, 0755), root-owned .ssh (0755), and an empty .ssh after verification; create runner-owned .config, .local, .cache and .local/state/brine (0700).",
		"Keep the home top level free of startup files and writable symlinks; require effective global SSH PermitUserEnvironment=no for loopback and remote connections and refuse per-user PAM environment files. Verify startup-file bypasses after enrollment.",
		"Enable lingering for the Brine runner and install the binary at /usr/local/bin/brine, with root-owned parents and mode 0755.",
		"Create root-owned /etc/ssh/brine and authorized_keys directories (0755), install the root-owned deploy key at /etc/ssh/brine/authorized_keys/brine (0644), and the inventory identity at /etc/ssh/brine/inventory-key (0644).",
		"Install and journal /etc/ssh/sshd_config.d/00-brine-brine.conf (root-owned 0644), forcing runner-only key sources and disabling PTY, tunnels and forwarding. Validate the staged complete SSH configuration and reload ssh.service; restore verified original state and reload on known failure, and require explicit undo after an unknown reload outcome.",
		"Install only the supplied public deploy key, restricted to the absolute binary path's host serve dispatcher, with no shell, PTY or forwarding access.",
		"Install a root-owned polkit rule granting only caddy.service reload, never restart or reload-or-restart.",
		"Create /etc/caddy/brine/gen-0 and current; add only import /etc/caddy/brine/current/*.caddy to the main Caddyfile; refuse other imports, validate, then unmask, enable and start Caddy.",
		"Record enrollment ownership, original Caddy configuration and service state in a root-owned journal; undo only recorded and verified changes.",
		"Write a private client target config with the authenticated SSH host key; verify the real restricted deploy key through ping and rerun read-only inventory.",
		"Make no firewall, DNS, Tailscale, other-user, app-data, root-helper or lifecycle policy changes; deploy access remains the dispatcher's typed read-only allowlist until later phases.",
	}}
	for _, name := range []string{"podman", "passt", "caddy"} {
		if f.Packages[name] == "" {
			p.MissingPackages = append(p.MissingPackages, name)
		}
	}
	return p, nil
}

func Confirm(input io.Reader, terminal, noInput bool, name string) error {
	if !terminal || noInput {
		return errors.New("enrollment requires operator confirmation from terminal stdin; --yes cannot authorize it")
	}
	line, err := bufio.NewReader(io.LimitReader(input, 128)).ReadString('\n')
	if err != nil || strings.TrimSuffix(strings.TrimSuffix(line, "\n"), "\r") != name {
		return errors.New("enrollment confirmation must exactly match the target name")
	}
	return nil
}
