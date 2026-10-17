package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ShaulLavo/brine/internal/cdn"
	"github.com/ShaulLavo/brine/internal/enroll"
	"github.com/ShaulLavo/brine/internal/policy"
	"github.com/ShaulLavo/brine/internal/transport"
)

func cdnPermissionFixture() cdn.Binding {
	profile := cdn.ProfileScope{Reference: "fixture", Driver: "local-http", Fingerprint: cdn.Hash([]byte("fixture-profile"))}
	return cdn.Binding{Reference: "public", App: "demo", Target: cdn.Hash([]byte("fixture-target")), Hostname: "demo.example.test",
		Origin:       cdn.Origin{Address: "https://192.0.2.5", Host: "demo.example.test", TLSName: "demo.example.test"},
		Cache:        cdn.CachePolicy{Prefix: "/objects/", Extensions: []string{"bin"}, TTLSeconds: cdn.ImmutableTTL},
		AllowedEdges: []cdn.ProfileScope{profile}, Traffic: profile,
		TrafficAuthority: cdn.TrafficAuthority{ResourceID: "fixture-record", Hostname: "demo.example.test", Before: cdn.Hash([]byte("fixture-before"))}}
}

func TestPermissionsCLIPlansTypedCDNBinding(t *testing.T) {
	binding := cdnPermissionFixture()
	raw, err := binding.CanonicalBytes()
	if err != nil {
		t.Fatal(err)
	}
	filename := filepath.Join(t.TempDir(), "binding.json")
	if err := os.WriteFile(filename, raw, 0600); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		flag string
		kind policy.PermissionKind
	}{{"--allow-cdn-binding", policy.AllowCDNBinding}, {"--revoke-cdn-binding", policy.RevokeCDNBinding}} {
		t.Run(test.flag, func(t *testing.T) { checkCDNPermissionCLIPlan(t, filename, raw, test.flag, test.kind) })
	}
}

func checkCDNPermissionCLIPlan(t *testing.T, filename string, want []byte, flag string, kind policy.PermissionKind) {
	t.Helper()
	var out, stderr bytes.Buffer
	deps := testDependencies(t, &out, &stderr)
	deps.LoadOperationTarget = func(string, string) (transport.Target, error) { return transport.Target{Name: "fixture"}, nil }
	calls := 0
	deps.PermissionClient = permissionCallFunc(func(_ context.Context, _ transport.Target, _, _ string, request enroll.PermissionsRequest) (enroll.PermissionsResponse, error) {
		calls++
		if len(request.Changes) != 1 || request.Changes[0].Kind != kind || request.Changes[0].CDNBinding == nil {
			t.Fatal("wrong typed CDN permission request")
		}
		got, err := request.Changes[0].CDNBinding.CanonicalBytes()
		if err != nil || !bytes.Equal(got, want) {
			t.Fatal("CLI changed CDN authority", err)
		}
		return enroll.PermissionsResponse{Plan: &enroll.PermissionsPlan{ID: strings.Repeat("a", 64), Changes: request.Changes}}, nil
	})
	if err := Execute(deps, []string{"permissions", "plan", "--target", "fixture", "--admin", "operator@fixture", flag, filename, "--json", "--no-input"}); err != nil || calls != 1 {
		t.Fatal("typed CDN plan failed", err, calls, stderr.String())
	}
	if !json.Valid(out.Bytes()) || !strings.Contains(out.String(), string(kind)) || !strings.Contains(out.String(), `"traffic_authority"`) {
		t.Fatal("plan output omitted typed CDN authority")
	}
}

func TestPermissionsCLIRejectsMalformedCDNBindingBeforeCall(t *testing.T) {
	raw, err := cdnPermissionFixture().CanonicalBytes()
	if err != nil {
		t.Fatal(err)
	}
	for _, malformed := range [][]byte{
		bytes.Replace(raw, []byte(`"reference":"public"`), []byte(`"Reference":"public"`), 1),
		bytes.Replace(raw, []byte(`"reference":"public"`), []byte(`"reference":"public","reference":"public"`), 1),
		bytes.Replace(raw, []byte(`"traffic":{`), []byte(`"traffic":{"credential":"do-not-echo",`), 1),
		append(bytes.Clone(raw), []byte(" {}")...),
		[]byte(strings.Repeat(" ", 64<<10+1)),
	} {
		checkMalformedCDNPermissionCLI(t, malformed)
	}
}

func checkMalformedCDNPermissionCLI(t *testing.T, raw []byte) {
	t.Helper()
	filename := filepath.Join(t.TempDir(), "binding.json")
	if err := os.WriteFile(filename, raw, 0600); err != nil {
		t.Fatal(err)
	}
	var out, stderr bytes.Buffer
	deps := testDependencies(t, &out, &stderr)
	calls := 0
	deps.PermissionClient = permissionCallFunc(func(context.Context, transport.Target, string, string, enroll.PermissionsRequest) (enroll.PermissionsResponse, error) {
		calls++
		return enroll.PermissionsResponse{}, nil
	})
	err := Execute(deps, []string{"permissions", "plan", "--target", "fixture", "--admin", "operator@fixture", "--allow-cdn-binding", filename, "--json", "--no-input"})
	if err == nil || calls != 0 || strings.Contains(out.String(), "do-not-echo") {
		t.Fatal("malformed CDN input reached the permission client or leaked input", err, calls)
	}
}
