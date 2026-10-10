package backupcredentials

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSecureCredentialReader(t *testing.T) {
	root := t.TempDir()
	if err := os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	}
	files := Files{Root: root}
	if _, err := files.Next("primary"); err != nil {
		t.Fatal(err)
	}
	path, err := files.Path("primary", 1)
	if err != nil {
		t.Fatal(err)
	}
	valid := "AWS_ACCESS_KEY_ID=PLANTED_KEY\nAWS_SECRET_ACCESS_KEY=PLANTED_SECRET\nAWS_SESSION_TOKEN=PLANTED_TOKEN\n"
	if err := os.WriteFile(path, []byte(valid), 0600); err != nil {
		t.Fatal(err)
	}
	p, err := files.ReadPath(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Environment()) != 3 {
		t.Fatal("session token missing")
	}
	b, _ := json.Marshal(p)
	if strings.Contains(string(b)+fmt.Sprintf("%v %#v", p, p), "PLANTED") {
		t.Fatal("reader leaked")
	}
	for _, bad := range []string{valid + "AWS_PROFILE=other\n", strings.Replace(valid, "AWS_SESSION_TOKEN=PLANTED_TOKEN", "AWS_ACCESS_KEY_ID=duplicate", 1), strings.Replace(valid, "PLANTED_KEY", "bad key", 1), strings.TrimSuffix(valid, "\n"), "AWS_ACCESS_KEY_ID=key\n"} {
		if err := os.WriteFile(path, []byte(bad), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := files.Read("primary", 1); err == nil {
			t.Fatal("accepted invalid environment")
		}
	}
	if err := os.WriteFile(path, []byte(valid), 0600); err != nil {
		t.Fatal(err)
	}
	for _, mode := range []os.FileMode{0644, 0400, 0660} {
		os.Chmod(path, mode)
		if _, err := files.Read("primary", 1); err == nil {
			t.Fatal("accepted mode", mode)
		}
	}
	os.Chmod(path, 0600)
	alias := filepath.Join(root, "alias")
	if err := os.Link(path, alias); err != nil {
		t.Fatal(err)
	}
	if _, err := files.Read("primary", 1); err == nil {
		t.Fatal("accepted hard link")
	}
	os.Remove(alias)
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(alias, path); err != nil {
		t.Fatal(err)
	}
	if _, err := files.Read("primary", 1); err == nil {
		t.Fatal("accepted symlink")
	}
	if _, err := files.ReadPath(filepath.Join(root, "..", "escape.env")); err == nil {
		t.Fatal("accepted escaped path")
	}
}
func TestCanceledDeliveryHasNoFiles(t *testing.T) {
	root := t.TempDir()
	os.Chmod(root, 0700)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	s := Service{Requester: "fixture-requester", Journal: &memoryJournal{plans: map[string]Plan{}}, Files: Files{Root: root}, Scope: func(context.Context, string) (Scope, error) { t.Fatal("scope after cancellation"); return Scope{}, nil }}
	if _, err := s.Plan(ctx, "hello", nil); err == nil {
		t.Fatal("accepted canceled plan")
	}
}
