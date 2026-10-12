package caddy

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/ShaulLavo/brine/internal/spec"
)

func TestManagedProspectiveLoggingChecksAbsentDomainsAndAllServerTrees(t *testing.T) {
	raw, err := os.ReadFile("testdata/managed-logging-protected.json")
	if err != nil {
		t.Fatal(err)
	}
	domains := []spec.Domain{"future.example.com"}
	if VerifyManagedLogging(raw, domains) == nil {
		t.Fatal("fixture unexpectedly has future route")
	}
	if err := VerifyProspectiveManagedLogging(raw, domains); err != nil {
		t.Fatal("conservative protected prerequisite refused", err)
	}
	var config map[string]any
	if err := json.Unmarshal(raw, &config); err != nil {
		t.Fatal(err)
	}
	servers := config["apps"].(map[string]any)["http"].(map[string]any)["servers"].(map[string]any)
	route := map[string]any{"match": []any{map[string]any{"host": []any{"unrelated.example.com"}}}, "handle": []any{map[string]any{"handler": "log_append", "fields": map[string]any{"query": "{http.request.uri.query}"}}}}
	servers["unrelated"] = map[string]any{"listen": []any{":8443"}, "routes": []any{route}}
	changed, err := json.Marshal(config)
	if err != nil {
		t.Fatal(err)
	}
	if VerifyProspectiveManagedLogging(changed, domains) == nil {
		t.Fatal("unknown prospective placement skipped unrelated receiver")
	}
	delete(servers, "unrelated")
	first := servers["srv0"].(map[string]any)
	first["named_routes"] = map[string]any{"credential-copy": route}
	changed, _ = json.Marshal(config)
	if VerifyProspectiveManagedLogging(changed, domains) == nil {
		t.Fatal("prospective namedtree credentialcopy admitted")
	}
}

func TestManagedProspectiveLoggingUsesPinnedNamedRouteShape(t *testing.T) {
	binary := pinnedCaddyVerifier(t)
	for _, unsafe := range []bool{false, true} {
		named := "respond 404"
		if unsafe {
			named = "log_append copied_uri {http.request.orig_uri}\n respond 404"
		}
		encoder := managedLogEncoder("delete")
		config := []byte("{\n admin off\n auto_https off\n log default {\n" + encoder + "}\n}\nhttp://127.0.0.1:23999 {\n log {\n" + encoder + "}\n invoke selected\n}\n&(selected) {\n" + named + "\n}\n")
		file := filepath.Join(t.TempDir(), "Caddyfile")
		if err := os.WriteFile(file, config, 0600); err != nil {
			t.Fatal(err)
		}
		adapted := runCaddyVerifier(t, binary, "adapt", "--config", file, "--adapter", "caddyfile")
		admitted := VerifyProspectiveManagedLogging(adapted, []spec.Domain{"future.example.com"}) == nil
		if admitted == unsafe {
			t.Fatal("actual named route logging prerequisite differs", unsafe)
		}
	}
}
