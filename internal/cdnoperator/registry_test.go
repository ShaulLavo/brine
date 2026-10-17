package cdnoperator

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ShaulLavo/brine/internal/cdn"
)

func registryDocument(t *testing.T, credential any) []byte {
	t.Helper()
	raw, err := json.Marshal(map[string]any{
		"schema_version": 1,
		"profiles": []any{map[string]any{
			"name": "edge", "driver": "cloudflare", "credential": credential,
			"configuration": map[string]any{
				"schema_version": 1, "account_id": strings.Repeat("a", 32), "zone_id": strings.Repeat("b", 32),
				"worker_prefix": "brine-cdn-test", "route_pattern": "https://app.example/*",
				"dns_record_id": strings.Repeat("c", 32), "credential_ref": "operator",
			},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestCDNRegistryEnvironmentCredentialIsExplicitAndReadAtUse(t *testing.T) {
	t.Setenv("BRINE_TEST_CDN_TOKEN", "first-token")
	registry, err := ParseRegistry(registryDocument(t, map[string]string{"kind": "environment", "name": "BRINE_TEST_CDN_TOKEN"}), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	profile := registry.profiles["edge"]
	if token, err := profile.tokens(t.Context(), "operator"); err != nil || token != "first-token" {
		t.Fatal("explicit environment reference did not resolve", err)
	}
	t.Setenv("BRINE_TEST_CDN_TOKEN", "refreshed-token")
	if token, err := profile.tokens(t.Context(), "operator"); err != nil || token != "refreshed-token" {
		t.Fatal("refreshed credential was not read at use", err)
	}
	if _, err := profile.tokens(t.Context(), "other"); err == nil {
		t.Fatal("crossed credential reference accepted")
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := profile.tokens(ctx, "operator"); err == nil {
		t.Fatal("cancelled credential read accepted")
	}
	wrong := profile.scope
	wrong.Fingerprint = cdn.Hash([]byte("different-native-scope"))
	if _, err := registry.Edge(wrong); err == nil {
		t.Fatal("unapproved native profile accepted")
	}
	if _, err := registry.Traffic(profile.scope); err != nil {
		t.Fatal(err)
	}
	t.Setenv("BRINE_TEST_CDN_TOKEN", "")
	if _, err := profile.tokens(t.Context(), "operator"); err == nil {
		t.Fatal("absent explicit credential accepted")
	}
}

func TestCDNRegistryFilePermissionsAndClosedCredentialVariants(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "profiles.json")
	raw := registryDocument(t, map[string]string{"kind": "file", "path": "token"})
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "token"), []byte("file-token\n"), 0600); err != nil {
		t.Fatal(err)
	}
	registry, err := LoadRegistry(path)
	if err != nil {
		t.Fatal(err)
	}
	if token, err := registry.profiles["edge"].tokens(t.Context(), "operator"); err != nil || token != "file-token" {
		t.Fatal("relative explicit token file unavailable", err)
	}
	if err := os.Chmod(path, 0644); err != nil { // #nosec G302 -- Exercise rejection of a public registry in a disposable fixture.
		t.Fatal(err)
	}
	if _, err := LoadRegistry(path); err == nil {
		t.Fatal("public profile registry accepted")
	}
	for _, credential := range []map[string]string{
		{"kind": "environment", "name": "BAD-NAME"},
		{"kind": "environment", "name": "TOKEN", "path": "token"},
		{"kind": "ambient"}, {"kind": "file", "path": ""},
	} {
		if _, err := ParseRegistry(registryDocument(t, credential), dir); err == nil {
			t.Fatal("invalid credential variant accepted", credential)
		}
	}
}
