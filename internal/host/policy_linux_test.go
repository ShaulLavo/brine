//go:build linux

package host

import (
	"context"
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
