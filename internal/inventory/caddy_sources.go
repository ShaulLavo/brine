package inventory

import (
	"path/filepath"
	"strings"
)

// Static file imports are enumerable. Snippet imports, placeholders, nested
// glob directories and ambiguous syntax remain unknown instead of losing sites.
func (c Collector) caddySources(root string) ([]string, bool) {
	paths := []string{}
	seen := map[string]bool{}
	var visit func(string, int) bool
	visit = func(path string, depth int) bool {
		path = filepath.Clean(path)
		if seen[path] || depth > 16 || len(paths) >= 128 {
			return false
		}
		seen[path] = true
		paths = append(paths, path)
		data, e := c.FS.ReadFile(path)
		if e != nil {
			return false
		}
		for _, line := range strings.Split(string(data), "\n") {
			line = strings.TrimSpace(line)
			if line == "" || strings.HasPrefix(line, "#") {
				continue
			}
			f := strings.Fields(line)
			for i, v := range f {
				if v == "import" && i != 0 {
					return false
				}
			}
			if f[0] != "import" {
				continue
			}
			if len(f) != 2 || strings.ContainsAny(f[1], `"{}()`) {
				return false
			}
			pattern := f[1]
			if !filepath.IsAbs(pattern) {
				pattern = filepath.Join(filepath.Dir(path), pattern)
			}
			dir, base := filepath.Dir(pattern), filepath.Base(pattern)
			if strings.ContainsAny(dir, "*?[") {
				return false
			}
			entries, e := c.FS.ReadDir(dir)
			if e != nil {
				return false
			}
			matched := false
			for _, entry := range entries {
				ok, e := filepath.Match(base, entry.Name())
				if e != nil {
					return false
				}
				if !ok || entry.IsDir() {
					continue
				}
				matched = true
				if !visit(filepath.Join(dir, entry.Name()), depth+1) {
					return false
				}
			}
			if !matched && !strings.ContainsAny(base, "*?[") {
				return false
			}
		}
		return true
	}
	ok := visit(root, 0)
	return paths, ok
}
