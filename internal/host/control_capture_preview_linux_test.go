//go:build linux

package host

import (
	"os"
	"path/filepath"
	"testing"
)

func TestControlCapturePreviewSymlinkReturnsUnavailable(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "state")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "target")
	if err := os.WriteFile(target, []byte("not a database"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(dir, "control.db")); err != nil {
		t.Fatal(err)
	}
	runtime, err := openPreviewState(t.Context(), dir)
	if err != nil {
		t.Fatalf("preview must retain structured preview_unavailable refusal, got %v", err)
	}
	t.Cleanup(func() { _ = runtime.Close() })
	report, err := runtime.Reconciler.DryRun(t.Context())
	if err != nil || report.ControlState != "preview_unavailable" {
		t.Fatalf("expected preview_unavailable, got %+v, error %v", report, err)
	}
}
