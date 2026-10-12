package caddy

import (
	"encoding/json"
	"os"
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
	first["named_routes"] = map[string]any{"credential-copy": []any{route}}
	changed, _ = json.Marshal(config)
	if VerifyProspectiveManagedLogging(changed, domains) == nil {
		t.Fatal("prospective namedtree credentialcopy admitted")
	}
}
