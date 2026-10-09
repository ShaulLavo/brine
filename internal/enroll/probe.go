package enroll

import (
	"context"
	"errors"
	"io/fs"
	"regexp"
	"strings"

	"github.com/ShaulLavo/brine/internal/inventory"
	"github.com/ShaulLavo/brine/internal/localexec"
)

type Prober struct {
	FS          inventory.FileSystem
	Runner      localexec.StdoutRunner
	IdentityKey []byte
	OwnedRunner func(context.Context) (bool, error)
}

func (p Prober) Collect(ctx context.Context) (Facts, error) {
	s, err := (inventory.Collector{FS: p.FS, Runner: p.Runner, IdentityKey: p.IdentityKey}).Collect(ctx)
	if err != nil {
		return Facts{}, err
	}
	f := Facts{Snapshot: s, Packages: map[string]string{}}
	key, err := p.FS.ReadFile(ctx, "/etc/ssh/ssh_host_ed25519_key.pub")
	if err != nil {
		return f, errors.New("cannot read host public key")
	}
	fields := strings.Fields(string(key))
	if len(fields) < 2 {
		return f, errors.New("invalid host public key")
	}
	f.HostKey = strings.Join(fields[:2], " ")
	for _, name := range []string{"podman", "passt", "caddy"} {
		v, err := installed(ctx, p.Runner, name)
		if err != nil {
			return f, err
		}
		if v != "" {
			f.Packages[name] = v
		}
	}

	missing := []string{}
	for _, name := range []string{"podman", "passt", "caddy"} {
		if f.Packages[name] == "" {
			missing = append(missing, name)
		}
	}
	if len(missing) > 0 {
		args := append([]string{"--simulate", "--no-remove", "--no-install-recommends", "install"}, missing...)
		out, err := p.Runner.RunStdout(ctx, "apt-get", args...)
		if err != nil {
			return f, errors.New("cannot preview apt transaction")
		}
		f.PackageInstall, err = aptInstalls(out)
		if len(f.PackageInstall) == 0 {
			return f, errors.New("apt preview did not bind the missing packages")
		}
		if err != nil {
			return f, err
		}
	}
	out, err := p.Runner.RunStdout(ctx, "/usr/sbin/sshd", "-T")
	if err != nil {
		return f, errors.New("effective sshd settings unavailable")
	}
	for _, line := range strings.Split(out, "\n") {
		fields := strings.Fields(line)
		if len(fields) == 2 && fields[0] == "permituserenvironment" {
			f.PermitUserEnvironment = fields[1]
		}
	}
	// Match blocks are not all represented by sshd -T. Conservatively reject
	// any enabling directive in the parsed configuration tree, even for other users.
	if err := p.sshConfig(ctx, "/etc/ssh/sshd_config", map[string]bool{}, 0); err != nil {
		return f, err
	}
	entries, err := p.FS.ReadDir(ctx, "/etc/pam.d")
	if err != nil {
		return f, errors.New("PAM settings unavailable")
	}
	for _, entry := range entries {
		if entry.IsDir() {
			return f, errors.New("PAM configuration directories unsupported")
		}
		data, err := p.FS.ReadFile(ctx, "/etc/pam.d/"+entry.Name())
		if err != nil {
			return f, errors.New("PAM settings unavailable")
		}
		for _, line := range strings.Split(string(data), "\n") {
			line = strings.SplitN(line, "#", 2)[0]
			if strings.Contains(line, "pam_env.so") && regexp.MustCompile(`\buser_readenv\s*=\s*1\b`).MatchString(line) {
				f.PAMUserEnvironment = true
			}
		}
	}
	f.PAMChecked = true
	if p.OwnedRunner != nil {
		f.OwnedRunner, err = p.OwnedRunner(ctx)
		if err != nil {
			return f, err
		}
	}
	return f, nil
}
func (p Prober) sshConfig(ctx context.Context, path string, seen map[string]bool, depth int) error {
	if depth > 16 || seen[path] {
		return errors.New("SSH configuration recursion refused")
	}
	seen[path] = true
	data, err := p.FS.ReadFile(ctx, path)
	if err != nil {
		return errors.New("SSH configuration unavailable")
	}
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(strings.SplitN(line, "#", 2)[0])
		if len(fields) == 0 {
			continue
		}
		switch strings.ToLower(fields[0]) {
		case "permituserenvironment":
			if len(fields) != 2 || fields[1] != "no" {
				return errors.New("SSH per-user environment is enabled")
			}
		case "include":
			// The Debian default glob is the only include form supported here. Other
			// forms need a real sshd source resolver, not an unchecked assumption.
			if len(fields) != 2 || fields[1] != "/etc/ssh/sshd_config.d/*.conf" {
				return errors.New("SSH include form unsupported for enrollment")
			}
			entries, err := p.FS.ReadDir(ctx, "/etc/ssh/sshd_config.d")
			if errors.Is(err, fs.ErrNotExist) {
				continue
			}
			if err != nil {
				return err
			}
			for _, entry := range entries {
				if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".conf") {
					if err := p.sshConfig(ctx, "/etc/ssh/sshd_config.d/"+entry.Name(), seen, depth+1); err != nil {
						return err
					}
				}
			}
		}
	}
	return nil
}
