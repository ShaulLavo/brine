package caddy

import (
	"bytes"
	"encoding/json"
	"os"
	"testing"
)

// Captured from the pinned v2.10.2 adapter, independently of expected route construction.
func TestManagedAdaptedSemanticOwnership(t *testing.T) {
	site := managedTestSite(t, true)
	raw, err := os.ReadFile("testdata/managed-adapted.json")
	if err != nil {
		t.Fatal(err)
	}
	next := map[string]Site{"web.caddy": site}
	if err := checkAdapted(raw, next, nil); err != nil {
		t.Fatal("pinned managed route refused", err)
	}
	var unprotected map[string]json.RawMessage
	if err := json.Unmarshal(raw, &unprotected); err != nil {
		t.Fatal(err)
	}
	delete(unprotected, "logging")
	unsafeRaw, err := json.Marshal(unprotected)
	if err != nil {
		t.Fatal(err)
	}
	if checkAdapted(unsafeRaw, next, nil) == nil {
		t.Fatal("exact route bypassed protected default logging")
	}
	changes := []struct{ name, old, new string }{
		{"engine destination", "127.0.0.1:20000", "127.0.0.1:20100"},
		{"frontend destination", "127.0.0.1:20001", "127.0.0.1:20101"},
		{"database", "database/game/subscribe", "database/foreign/subscribe"},
		{"token authorization", "^Bearer .+$", ".*"},
		{"reserved denial", "\"status_code\":403", "\"status_code\":200"},
		{"token duplication", "size() == 1", "size() >= 1"},
		{"exclusive group", "\"group\":\"group5\"", "\"group\":\"other-group\""},
	}
	for _, change := range changes {
		t.Run(change.name, func(t *testing.T) {
			if !bytes.Contains(raw, []byte(change.old)) {
				t.Fatal("fixture mutation target absent")
			}
			changed := bytes.Replace(raw, []byte(change.old), []byte(change.new), 1)
			if checkAdapted(changed, next, nil) == nil {
				t.Fatal("altered managed route admitted")
			}
		})
	}
	alias := bytes.ReplaceAll(raw, []byte("\"group5\""), []byte("\"renamed-by-adapter\""))
	if err := checkAdapted(alias, next, nil); err != nil {
		t.Fatal("consistent generated group rename refused", err)
	}
	site.managed.appPort = 0
	if checkAdapted(raw, map[string]Site{"web.caddy": site}, nil) == nil {
		t.Fatal("frontend route admitted for engine-only ownership")
	}
}
