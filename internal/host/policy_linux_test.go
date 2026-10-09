//go:build linux

package host

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func TestPolicyRejectsRunnerOwnedFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "policy.toml")
	if err := os.WriteFile(path, []byte("schema_version = 1"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := (DiskPolicy{Path: path}).Load(context.Background()); err == nil {
		t.Fatal("untrusted policy accepted")
	}
}

func TestProductionPolicyRequiresSupportedHTTPSListener(t *testing.T) {
	fixture, err := os.ReadFile("../policy/testdata/operator.toml")
	if err != nil {
		t.Fatal(err)
	}
	for _, port := range []int{0, 22, 80, 443, 8443, 20000} {
		raw := append([]byte(fmt.Sprintf("caddy_port = %d\n", port)), fixture...)
		_, err := productionPolicy(raw)
		if (err == nil) != (port == 443) {
			t.Fatalf("port=%d error=%v", port, err)
		}
	}
}
