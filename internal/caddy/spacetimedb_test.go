package caddy

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ShaulLavo/brine/internal/plan"
	"github.com/ShaulLavo/brine/internal/policy"
	"github.com/ShaulLavo/brine/internal/spec"
)

func managedPreview(t testing.TB, frontend bool) plan.ManagedRenderPreview {
	t.Helper()
	raw, err := os.ReadFile("../spec/testdata/managed.toml")
	if err != nil {
		t.Fatal(err)
	}
	if frontend {
		raw = []byte("image=\"registry.example.com:5000/team/web@sha256:" + strings.Repeat("a", 64) + "\"\ncontainer_port=8080\nresources={memory_mb=128,pids_limit=128}\n" + string(raw))
	}
	a, err := spec.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	raw, err = os.ReadFile("../policy/testdata/managed-operator.toml")
	if err != nil {
		t.Fatal(err)
	}
	p, err := policy.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	d, err := policy.Normalize(a, p)
	if err != nil {
		t.Fatal(err)
	}
	appPort := spec.Port(0)
	if frontend {
		appPort = 20001
	}
	preview, err := plan.NewManagedRenderPreview(d, p, 20000, appPort)
	if err != nil {
		t.Fatal(err)
	}
	return preview
}

func TestManagedPreviewAdaptsWithPinnedCaddy(t *testing.T) {
	binary := pinnedCaddyVerifier(t)
	for _, frontend := range []bool{false, true} {
		raw, err := RenderManagedPreview(managedPreview(t, frontend))
		if err != nil {
			t.Fatal(err)
		}
		file := filepath.Join(t.TempDir(), "Caddyfile")
		if err := os.WriteFile(file, raw, 0o600); err != nil {
			t.Fatal(err)
		}
		output := runCaddyVerifier(t, binary, "adapt", "--config", file, "--adapter", "caddyfile")
		if !json.Valid(output) || !bytes.Contains(output, []byte("/v1/identity/websocket-token")) {
			t.Fatal("missing adapted client routes")
		}
	}
}

func pinnedCaddyVerifier(t *testing.T) string {
	t.Helper()
	binary := os.Getenv("BRINE_TEST_CADDY")
	if binary == "" {
		t.Skip("explicit local Caddy verifier not provided")
	}
	if !filepath.IsAbs(binary) {
		t.Fatal("Caddy verifier must be an absolute path")
	}
	if !bytes.HasPrefix(runCaddyVerifier(t, binary, "version"), []byte("v2.10.2 ")) {
		t.Fatal("Caddy verifier must be pinned to v2.10.2")
	}
	return binary
}

func runCaddyVerifier(t *testing.T, binary string, args ...string) []byte {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	command := caddyVerifierCommand(t, ctx, binary, args...)
	output, err := command.Output()
	if err != nil {
		t.Fatal("Caddy verifier failed", err)
	}
	return output
}

func caddyVerifierCommand(t *testing.T, ctx context.Context, binary string, args ...string) *exec.Cmd {
	t.Helper()
	// #nosec G204 G702 -- Explicit local test opt-in selects the verifier; production previews launch no process.
	command := exec.CommandContext(ctx, binary, args...)
	command.Env = []string{"PATH=/usr/bin:/bin", "HOME=" + t.TempDir()}
	return command
}

func TestManagedClientRouteAllowlist(t *testing.T) {
	ingress, err := NewManagedClientIngress(managedPreview(t, true))
	if err != nil {
		t.Fatal(err)
	}
	base := "/_brine/db/main/v1/"
	if !ingress.Allows("GET", base+"database/game/subscribe?token=client", "", "websocket") || !ingress.Allows("POST", base+"identity/websocket-token", "Bearer client", "") {
		t.Fatal("required official SDK route refused")
	}
	for _, path := range []string{"database/game", "database/game/sql", "database/game/pre_publish", "database/game/environment", "database/game/schema", "database/game/names", "database/game/logs", "database/other/subscribe?token=client", "identity", "health", "database/game/subscribe/extra?token=client", "database/game/subscribe?token=", "database/game/subscribe?token=one&token=two", "database/game/%73ubscribe?token=client", "database/game/../game/subscribe?token=client"} {
		for _, method := range []string{"GET", "POST", "PUT", "PATCH", "DELETE"} {
			if ingress.Allows(method, base+path, "Bearer client", "websocket") {
				t.Fatalf("management/unknown route allowed: %s %s", method, path)
			}
		}
	}
	if ingress.Allows("GET", base+"database/game/subscribe", "", "websocket") || ingress.Allows("GET", base+"database/game/subscribe?token=client", "", "") || ingress.Allows("POST", base+"identity/websocket-token", "", "") || ingress.Allows("POST", base+"identity/websocket-token", "Bearer ", "") {
		t.Fatal("anonymous or non-WS route allowed")
	}
}

func TestManagedCaddyPreviewOrdersDenyBeforeFrontend(t *testing.T) {
	for _, frontend := range []bool{false, true} {
		raw, err := RenderManagedPreview(managedPreview(t, frontend))
		if err != nil {
			t.Fatal(err)
		}
		deny := bytes.Index(raw, []byte("respond 403"))
		fallback := bytes.Index(raw, []byte("reverse_proxy 127.0.0.1:20001"))
		if deny < 0 || frontend && fallback < deny || !frontend && !bytes.Contains(raw, []byte("respond 404")) {
			t.Fatal("managed denial did not precede fallback")
		}
		if bytes.Contains(raw, []byte("/v1/*")) || !bytes.Contains(raw, []byte("uri strip_prefix /_brine/db/main")) || !bytes.Contains(raw, []byte("lowerAscii() == 'websocket'")) || !bytes.Contains(raw, []byte("Authorization")) {
			t.Fatal("missing typed route/auth boundaries")
		}
	}
	if _, err := RenderManagedPreview(plan.ManagedRenderPreview{}); err == nil {
		t.Fatal("zero preview rendered")
	}
}
