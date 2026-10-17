//go:build linux

package enroll

import (
	"os"
	"path/filepath"
	"testing"
)

func TestPermissionsJournalRejectsUntrustedMetadata(t *testing.T) {
	path := filepath.Join(t.TempDir(), "record.json")
	if err := os.WriteFile(path, []byte("{}"), 0600); err != nil {
		t.Fatal(err)
	}
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	if err = permissionRecordMetadata(keyOwnershipInfo{info, 0}); err != nil {
		t.Fatal(err)
	}
	if err = permissionRecordMetadata(keyOwnershipInfo{info, 1000}); err == nil {
		t.Fatal("runner-owned journal accepted")
	}
	if err = os.Chmod(path, 0644); err != nil {
		t.Fatal(err)
	}
	info, err = os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	if err = permissionRecordMetadata(keyOwnershipInfo{info, 0}); err == nil {
		t.Fatal("public journal accepted")
	}
	if err = os.Chmod(path, 0600); err != nil {
		t.Fatal(err)
	}
	if err = os.Link(path, path+"-hardlink"); err != nil {
		t.Fatal(err)
	}
	info, err = os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	if err = permissionRecordMetadata(keyOwnershipInfo{info, 0}); err == nil {
		t.Fatal("hardlinked journal accepted")
	}
	if err = os.Symlink(path, path+"-symlink"); err != nil {
		t.Fatal(err)
	}
	info, err = os.Lstat(path + "-symlink")
	if err != nil {
		t.Fatal(err)
	}
	if err = permissionRecordMetadata(keyOwnershipInfo{info, 0}); err == nil {
		t.Fatal("symlink journal accepted")
	}
}
func TestPermissionsHostRequiresRootAndEnrollmentIdentity(t *testing.T) {
	if _, err := PermissionsHostOperation(t.Context(), PermissionsRequest{Action: "show"}); err == nil {
		t.Fatal("missing root enrollment identity accepted")
	}
	if os.Geteuid() == 0 {
		return
	}
	if _, err := PermissionsHostOperation(t.Context(), PermissionsRequest{Action: "show", IdentityKey: make([]byte, 32)}); err == nil {
		t.Fatal("non-root operator handler accepted")
	}
}
