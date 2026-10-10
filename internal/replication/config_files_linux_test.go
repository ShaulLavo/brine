//go:build linux

package replication

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDiskConfigFilesRequirePrivatePinnedRegularFile(t *testing.T) {
	root := t.TempDir()
	if err := os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(root, "litestream.yml")
	if err := os.WriteFile(file, []byte("private config"), 0600); err != nil {
		t.Fatal(err)
	}
	r := DiskConfigs{}
	ctx := context.Background()
	raw, err := r.ReadConfig(ctx, file)
	if err != nil || string(raw) != "private config" {
		t.Fatal("safe read failed", err)
	}
	if err := os.Chmod(file, 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := r.ReadConfig(ctx, file); err == nil {
		t.Fatal("unsafe mode admitted")
	}
	info, _ := os.Stat(file)
	if info.Mode().Perm() != 0644 {
		t.Fatal("reader repaired permissions")
	}
	if err := os.Chmod(file, 0600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "hardlink")
	if err := os.Link(file, link); err != nil {
		t.Fatal(err)
	}
	if _, err := r.ReadConfig(ctx, file); err == nil {
		t.Fatal("multiple links admitted")
	}
	if err := os.Remove(link); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(file, link); err != nil {
		t.Fatal(err)
	}
	if _, err := r.ReadConfig(ctx, link); err == nil {
		t.Fatal("symlink file admitted")
	}
	alias := filepath.Join(root, "alias")
	if err := os.Symlink(root, alias); err != nil {
		t.Fatal(err)
	}
	if _, err := r.ReadConfig(ctx, filepath.Join(alias, "litestream.yml")); err == nil {
		t.Fatal("symlink ancestor admitted")
	}
	if err := os.WriteFile(file, []byte(strings.Repeat("x", MaxConfigBytes+1)), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := r.ReadConfig(ctx, file); err == nil {
		t.Fatal("unbounded config admitted")
	}
	if err := os.Chmod(root, 0755); err != nil {
		t.Fatal(err)
	}
	if _, err := r.ReadConfig(ctx, file); err == nil {
		t.Fatal("public parent admitted")
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := r.ReadConfig(canceled, file); err == nil {
		t.Fatal("canceled config read admitted")
	}
}
