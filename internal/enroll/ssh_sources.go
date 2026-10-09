package enroll

import (
	"context"
	"errors"
	"io/fs"
	"path/filepath"
	"strings"
)

// Environment lists are additive: unlike the forced policy, an early Match
// cannot override unsafe entries. Audit every included branch conservatively,
// even branches for other users, so synthetic -C samples are not the boundary.
func (p Prober) sshEnvironmentSources(ctx context.Context, path string) error {
	count, total := 0, 0
	seen := map[string]bool{}
	var visit func(string, int, bool) error
	visit = func(path string, depth int, required bool) error {
		if depth > 16 {
			return errors.New("SSH environment include depth exceeded")
		}
		if seen[path] {
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
				if len(fields) < 2 {
					return errors.New("SSH environment include incomplete")
				}
				for _, pattern := range fields[1:] {
					if !filepath.IsAbs(pattern) {
						pattern = filepath.Join("/etc/ssh", pattern)
					}
					if !strings.ContainsAny(pattern, "*?[") {
						if err := visit(filepath.Clean(pattern), depth+1, false); err != nil {
							return err
						}
						continue
					}
					dir, base := filepath.Dir(pattern), filepath.Base(pattern)
					if strings.ContainsAny(dir, "*?[") {
						return errors.New("SSH environment include directory glob unsupported")
					}
					entries, err := p.FS.ReadDir(ctx, dir)
					if errors.Is(err, fs.ErrNotExist) {
						continue
					}
					if err != nil {
						return errors.New("SSH environment include directory unavailable")
					}
					for _, entry := range entries {
						matched, err := filepath.Match(base, entry.Name())
						if err != nil {
							return errors.New("SSH environment include pattern unsupported")
						}
						if matched {
							if err := visit(filepath.Join(dir, entry.Name()), depth+1, true); err != nil {
								return err
							}
						}
					}
				}
			}
		}
		return nil
	}
	return visit(path, 0, true)
}

// Tokenize only enough OpenSSH syntax to find includes and additive environment
// directives. Unbalanced quotes/escapes fail closed, never silently skip input.
func sshSourceFields(line string) ([]string, error) {
	var fields []string
	var token strings.Builder
	quoted, escaped, started := false, false, false
	flush := func() {
		if started {
			fields = append(fields, token.String())
			token.Reset()
			started = false
		}
	}
	for _, c := range line {
		if escaped {
			token.WriteRune(c)
			started = true
			escaped = false
			continue
		}
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
	if quoted || escaped {
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
