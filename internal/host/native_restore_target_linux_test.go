//go:build linux

package host

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"github.com/ShaulLavo/brine/internal/data"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNativeRestoreTargetCopiesRootedAuthenticatedMemberAndPreservesDownload(t *testing.T) {
	ctx := context.Background()
	parent := t.TempDir()
	for _, name := range []string{"download", "staged"} {
		if err := os.Mkdir(filepath.Join(parent, name), 0700); err != nil {
			t.Fatal(err)
		}
	}
	download, err := engineOpenRoot(filepath.Join(parent, "download"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = download.Close() }()
	staged, err := engineOpenRoot(filepath.Join(parent, "staged"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = staged.Close() }()
	raw := []byte("retained native checkpoint")
	if err = download.Mkdir("control-db", 0700); err != nil {
		t.Fatal(err)
	}
	if err = download.WriteFile("control-db/data", raw, 0600); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(raw)
	source := &nativeRestoredSource{root: download, limits: engineCopyLimits{Bytes: 1024, Files: 16, Depth: 8}}
	if err = copyNativeRestoreMember(ctx, source, staged, "control-db/data", uint64(len(raw)), hex.EncodeToString(sum[:])); err != nil {
		t.Fatal(err)
	}
	if err = syncNativeRestoreTarget(ctx, staged, source.limits); err != nil {
		t.Fatal(err)
	}
	for _, root := range []*os.Root{download, staged} {
		actual, readErr := root.ReadFile("control-db/data")
		if readErr != nil || string(actual) != string(raw) {
			t.Fatal("native copy changed authenticated bytes", readErr)
		}
	}
	if err = copyNativeRestoreMember(ctx, source, staged, "control-db/data", uint64(len(raw)), hex.EncodeToString(sum[:])); err == nil {
		t.Fatal("partial target was overwritten")
	}
}

func TestNativeRestoreTargetRefusesChangedSourceAndSymlinkParent(t *testing.T) {
	ctx := context.Background()
	parent := t.TempDir()
	for _, name := range []string{"download", "staged"} {
		if err := os.Mkdir(filepath.Join(parent, name), 0700); err != nil {
			t.Fatal(err)
		}
	}
	download, err := engineOpenRoot(filepath.Join(parent, "download"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = download.Close() }()
	staged, err := engineOpenRoot(filepath.Join(parent, "staged"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = staged.Close() }()
	source := &nativeRestoredSource{root: download, limits: engineCopyLimits{Bytes: 1024, Files: 16, Depth: 8}}
	if err = download.WriteFile("metadata.toml", []byte("mutated"), 0600); err != nil {
		t.Fatal(err)
	}
	expected := sha256.Sum256([]byte("original"))
	if err = copyNativeRestoreMember(ctx, source, staged, "metadata.toml", 7, hex.EncodeToString(expected[:])); err == nil {
		t.Fatal("changed source member copied")
	}
	if err = os.Symlink(filepath.Join(parent, "download"), filepath.Join(parent, "staged", "control-db")); err != nil {
		t.Fatal(err)
	}
	if err = copyNativeRestoreMember(ctx, source, staged, "control-db/secret", 7, hex.EncodeToString(expected[:])); err == nil {
		t.Fatal("symlink target parent admitted")
	}
}

func TestNativeRestoreSigningUsesExactRetainedOrEncryptedMember(t *testing.T) {
	ctx := context.Background()
	path := t.TempDir()
	for _, name := range []string{"state", "download"} {
		if err := os.Mkdir(filepath.Join(path, name), 0700); err != nil {
			t.Fatal(err)
		}
	}
	state, err := engineOpenRoot(filepath.Join(path, "state"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = state.Close() }()
	download, err := engineOpenRoot(filepath.Join(path, "download"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = download.Close() }()
	raw := []byte("exact retained signing member")
	sum := sha256.Sum256(raw)
	runtime := data.EngineRuntime{BindingID: data.ReplicaBindingID(strings.Repeat("a", 32))}
	runtime.Secrets.SigningPrivate = data.EngineSecretVersion{Reference: "signing", Version: 7, SHA256: hex.EncodeToString(sum[:])}
	name := filepath.Join("engine-secrets", string(runtime.BindingID), "signing-private", "v7.secret")
	if err = engineMakePrivateDirectories(state, filepath.Dir(name)); err != nil {
		t.Fatal(err)
	}
	if err = state.WriteFile(name, raw, 0600); err != nil {
		t.Fatal(err)
	}
	if err = download.Mkdir("recovery", 0700); err != nil {
		t.Fatal(err)
	}
	if err = download.WriteFile("recovery/signing.key", raw, 0600); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ kind, member string }{{"external_immutable", ""}, {"encrypted_member", "recovery/signing.key"}} {
		got, err := readNativeRestoreSigningPrivate(ctx, state, download, runtime, tc.kind, tc.member)
		if err != nil || string(got) != string(raw) {
			t.Fatal("retained signing recovery failed", err)
		}
		clear(got)
	}
	runtime.Secrets.SigningPrivate.Version = 8
	if _, err = readNativeRestoreSigningPrivate(ctx, state, download, runtime, "external_immutable", ""); err == nil {
		t.Fatal("missing selected version substituted")
	}
	for _, tc := range []struct{ kind, member string }{{"external_immutable", "recovery/signing.key"}, {"encrypted_member", "../key"}, {"unknown", ""}} {
		if _, err = readNativeRestoreSigningPrivate(ctx, state, download, runtime, tc.kind, tc.member); err == nil {
			t.Fatal("unqualified signing recovery admitted")
		}
	}
}
