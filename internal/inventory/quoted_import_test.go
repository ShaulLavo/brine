package inventory

import (
	"io/fs"
	"testing"

	"github.com/ShaulLavo/brine/internal/target"
)

func TestQuotedImportInGenerationFileRefusesProvenance(t *testing.T) {
	f, r := productionRoutes(t, false)
	f.files["/etc/caddy/brine/current/other.caddy"] = "(unused) {\n \"import\" /etc/caddy/brine/current/*.caddy\n}\n"
	f.dirs["/etc/caddy/brine/current"] = []fs.DirEntry{fixtureEntry("hello.caddy"), fixtureEntry("other.caddy")}
	r["caddy adapt --config /etc/caddy/brine/current/other.caddy --adapter caddyfile"] = "{}"
	s := measureRoutes(t, f, r)
	if s.LiveCaddyFiles.Status != target.Unknown {
		t.Fatalf("nested quoted import allowed attribution: %+v", s.LiveCaddyFiles)
	}
}
