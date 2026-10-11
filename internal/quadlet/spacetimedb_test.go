package quadlet

import (
	"bytes"
	"os"
	"strings"
	"testing"

	"github.com/ShaulLavo/brine/internal/plan"
	"github.com/ShaulLavo/brine/internal/policy"
	"github.com/ShaulLavo/brine/internal/spec"
)

func managedUnitPreview(t testing.TB, frontend bool) plan.ManagedRenderPreview {
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
	port := spec.Port(0)
	if frontend {
		port = 20001
	}
	preview, err := plan.NewManagedRenderPreview(d, p, 20000, port)
	if err != nil {
		t.Fatal(err)
	}
	return preview
}

func TestManagedServiceUsesOnlyTypedSupervisor(t *testing.T) {
	u, err := RenderManagedServicePreview(managedUnitPreview(t, false), "0123456789abcdef0123456789abcdef")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(u.Name(), ".service") || !bytes.Contains(u.Bytes(), []byte("ExecStart=/usr/local/bin/brine host engine-exec 0123456789abcdef0123456789abcdef")) || bytes.Contains(u.Bytes(), []byte("ExecStartPre=")) || bytes.Contains(u.Bytes(), []byte("podman")) {
		t.Fatal("engine start bypasses typed supervisor")
	}
	for _, setting := range []string{"KillSignal=SIGINT", "TimeoutStopSec=30", "MemoryMax=128M", "TasksMax=128", "UMask=0077"} {
		if !bytes.Contains(u.Bytes(), []byte(setting)) {
			t.Fatal("missing bounded service setting", setting)
		}
	}
	if _, err := RenderManagedServicePreview(managedUnitPreview(t, false), "../unsafe"); err == nil {
		t.Fatal("unsafe binding accepted")
	}
}

func TestManagedFrontendHasOnlyPublicConnectionSettings(t *testing.T) {
	u, err := RenderManagedFrontendPreview(managedUnitPreview(t, true))
	if err != nil {
		t.Fatal(err)
	}
	for _, setting := range []string{"BRINE_DB_MAIN_ENGINE=spacetimedb", "BRINE_DB_MAIN_NAME=game", "BRINE_DB_MAIN_ACCEPTS=game-v1,game-v2", "BRINE_DB_MAIN_URL=https://other.example.net/_brine/db/main/", "PublishPort=127.0.0.1:20001:8080"} {
		if !bytes.Contains(u.Bytes(), []byte(setting)) {
			t.Fatal("missing public connection setting", setting)
		}
	}
	for _, private := range []string{"BRINE_DB_MAIN_PATH", "Volume=", "publisher", "signing-private", "Secret="} {
		if bytes.Contains(u.Bytes(), []byte(private)) {
			t.Fatal("engine material reached frontend", private)
		}
	}
	if _, err := RenderManagedFrontendPreview(managedUnitPreview(t, false)); err == nil {
		t.Fatal("dummy frontend rendered")
	}
}
