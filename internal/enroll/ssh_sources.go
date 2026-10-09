package enroll

import (
	"context"
	"errors"
	"io/fs"
	"path/filepath"
	"slices"
	"sort"
	"strings"
)

// Environment lists are additive: unlike the forced policy, an early Match
// cannot override unsafe entries. Audit every included branch conservatively,
// even branches for other users, so synthetic -C samples are not the boundary.
func (p Prober) sshEnvironmentSources(ctx context.Context, path string) error {
	_, err := p.sshAuditedSources(ctx, path)
	return err
}

func (p Prober) sshAuditedSources(ctx context.Context, path string) ([]string, error) {
	mainPath := path
	var sources []string
	count, total := 0, 0
	seen := map[string]bool{}
	var visit func(string, int, bool) error
	visit = func(path string, depth int, required bool) error {
		if depth > 1 {
			return errors.New("SSH environment include depth exceeded")
		}
		if seen[path] {
			if path == mainPath && depth != 0 {
				return errors.New("SSH recursive main include unsupported")
			}
			return nil
		}
		seen[path] = true
		data, err := p.FS.ReadFile(ctx, path)
		if errors.Is(err, fs.ErrNotExist) && !required {
			return nil
		}
		if err != nil {
			return errors.New("SSH environment configuration unavailable")
		}
		sources = append(sources, path)
		count++
		total += len(data)
		if count > 128 || total > 4<<20 {
			return errors.New("SSH environment configuration bounds exceeded")
		}
		for _, line := range strings.Split(string(data), "\n") {
			fields, err := sshSourceFields(line)
			if err != nil {
				return err
			}
			if len(fields) == 0 {
				continue
			}
			switch fields[0] {
			case "acceptenv", "setenv":
				if len(fields) < 2 {
					return errors.New("SSH environment directive incomplete")
				}
				if err := checkSSHEnvironment(strings.Join(fields, " ")); err != nil {
					return err
				}
			case "include":
				if depth != 0 {
					return errors.New("SSH nested includes unsupported")
				}
				if len(fields) < 2 {
					return errors.New("SSH environment include incomplete")
				}
				for _, pattern := range fields[1:] {
					if pattern == "/etc/ssh/sshd_config.d/*.conf" {
						const dir = "/etc/ssh/sshd_config.d"
						entries, err := p.FS.ReadDir(ctx, dir)
						if errors.Is(err, fs.ErrNotExist) {
							continue
						}
						if err != nil {
							return errors.New("SSH environment include directory unavailable")
						}
						var names []string
						// glob(3) excludes leading dots; LC_ALL=C gives bytewise ordering.
						for _, entry := range entries {
							name := entry.Name()
							if !strings.HasPrefix(name, ".") && strings.HasSuffix(name, ".conf") {
								names = append(names, name)
							}
						}
						sort.Strings(names)
						for _, name := range names {
							if err := visit(filepath.Join(dir, name), 1, true); err != nil {
								return err
							}
						}
					} else {
						if !filepath.IsAbs(pattern) || strings.ContainsAny(pattern, "*?[]{}~") {
							return errors.New("SSH environment include pattern unsupported")
						}
						if err := visit(pattern, 1, false); err != nil {
							return err
						}
					}
				}
			}
		}
		return nil
	}
	err := visit(path, 0, true)
	return sources, err
}

// Tokenize only enough OpenSSH syntax to find includes and additive environment
// directives. Unbalanced quotes/escapes fail closed, never silently skip input.
func sshSourceFields(line string) ([]string, error) {
	var fields []string
	var token strings.Builder
	quoted, started := false, false
	flush := func() {
		if started {
			fields = append(fields, token.String())
			token.Reset()
			started = false
		}
	}
	for _, c := range line {
		if c == '\\' {
			return nil, errors.New("SSH environment source escapes unsupported")
		}
		if c == '"' {
			quoted = !quoted
			started = true
			continue
		}
		if !quoted && !started && c == '#' {
			break
		}
		if !quoted && (c == ' ' || c == '\t' || c == '\r') {
			flush()
			continue
		}
		token.WriteRune(c)
		started = true
	}
	if quoted {
		return nil, errors.New("SSH environment source syntax unsupported")
	}
	flush()
	if len(fields) == 0 {
		return nil, nil
	}
	if key, value, ok := strings.Cut(fields[0], "="); ok {
		fields[0] = key
		if value != "" {
			fields = append(fields[:1], append([]string{value}, fields[1:]...)...)
		}
	}
	if len(fields) > 1 && strings.HasPrefix(fields[1], "=") {
		fields[1] = strings.TrimPrefix(fields[1], "=")
		if fields[1] == "" {
			fields = append(fields[:1], fields[2:]...)
		}
	}
	fields[0] = strings.ToLower(fields[0])
	return fields, nil
}

// Compare unique load order, not reprocessing messages, with the audited source manifest.
func checkSSHSourceTrace(sources []string, trace string) error {
	var loaded []string
	seen := map[string]bool{}
	const prefix = "debug2: load_server_config: filename "
	for _, line := range strings.Split(trace, "\n") {
		if path, ok := strings.CutPrefix(strings.TrimSuffix(line, "\r"), prefix); ok {
			if !seen[path] {
				seen[path] = true
				loaded = append(loaded, path)
			}
		}
	}
	if len(sources) == 0 || !slices.Equal(sources, loaded) {
		return errors.New("SSH loaded source manifest differs from audit")
	}
	return nil
}
