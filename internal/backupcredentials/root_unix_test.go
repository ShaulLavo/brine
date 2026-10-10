//go:build linux || darwin

package backupcredentials

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestCredentialRootSubstitutionRefused(t *testing.T) {
	base := t.TempDir()
	path := filepath.Join(base, "credentials")
	if err := os.Mkdir(path, 0700); err != nil {
		t.Fatal(err)
	}
	pinned, err := pinCredentialRoot(path)
	if err != nil {
		t.Fatal(err)
	}
	defer pinned.Close()
	identity, err := pinned.Stat()
	if err != nil {
		t.Fatal(err)
	}
	// Substitute the pathname between the pinned walk and os.OpenRoot.
	if err := os.Rename(path, path+"-original"); err != nil {
		t.Fatal(err)
	}
	replacement := filepath.Join(base, "replacement")
	if err := os.Mkdir(replacement, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(replacement, path); err != nil {
		t.Fatal(err)
	}
	root, err := openMatchingCredentialRoot(path, identity)
	if root != nil {
		root.Close()
		t.Fatal("accepted substituted credential root")
	}
	if !errors.Is(err, ErrStorage) {
		t.Fatal("substitution did not require reconciliation")
	}
}

func TestCredentialRootSymlinkedAncestorRefused(t *testing.T) {
	base := t.TempDir()
	real := filepath.Join(base, "real")
	if err := os.Mkdir(real, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(real, "credentials"), 0700); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(base, "alias")
	if err := os.Symlink(real, alias); err != nil {
		t.Fatal(err)
	}
	pinned, err := pinCredentialRoot(filepath.Join(alias, "credentials"))
	if pinned != nil {
		pinned.Close()
		t.Fatal("followed symlinked ancestor")
	}
	if !errors.Is(err, ErrStorage) {
		t.Fatal("unsafe ancestry did not require reconciliation")
	}
}
