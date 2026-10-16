//go:build linux

package host

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/ShaulLavo/brine/internal/caddy"
)

func TestLiveRestoreUsesPreparedWithdrawalInsteadOfHistoricalServingReceipt(t *testing.T) {
	r := newNativeWithdrawalRig(t)
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "gen-2"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("gen-2", filepath.Join(root, "current")); err != nil {
		t.Fatal(err)
	}
	adapter := &withdrawalCaddy{adapted: []byte(`{"apps":{"http":{"servers":{}}}}`)}
	manager, err := caddy.NewManager(root, adapter, adapter)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = manager.Close() })
	runtime := nativeBackupRuntime{state: r.state, routes: manager}
	if err := runtime.verifyRestoreIngress(t.Context(), r.service, "4a88ac96c16d0c23e0f63c9832ac998b", r.original.App, r.original.Managed.Database.BindingID); err != nil {
		t.Fatal("completed withdrawal refused", err)
	}
	adapter.adapted = r.adapted
	if err := runtime.verifyRestoreIngress(t.Context(), r.service, "4a88ac96c16d0c23e0f63c9832ac998b", r.original.App, r.original.Managed.Database.BindingID); err == nil {
		t.Fatal("loaded serving route accepted")
	}
}
