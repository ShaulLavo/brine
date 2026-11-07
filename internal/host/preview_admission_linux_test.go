//go:build linux

package host

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/ShaulLavo/brine/internal/admission"
)

func TestPreviewPreservesAdmissionUnavailable(t *testing.T) {
	for _, name := range []string{"state.json", "lock"} {
		t.Run(name, func(t *testing.T) { previewMissingAdmission(t, name) })
	}
}
func previewMissingAdmission(t *testing.T, name string) {
	t.Helper()
	gateDirectory(t)
	dir := gateStateDir(t)
	state, err := openState(t.Context(), dir, stateMutating, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := state.Close(); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(admissionRoot, name)
	if err := os.Rename(path, path+".test-saved"); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := os.Rename(path+".test-saved", path); err != nil {
			t.Error(err)
		}
	}()
	runtime, err := openPreviewState(t.Context(), dir)
	defer closeAdmissionPreview(t, runtime)
	if !errors.Is(err, admission.ErrUnavailable) {
		if runtime == nil {
			t.Fatalf("missing admission became unrelated failure: %v", err)
		}
		report, reportErr := runtime.Reconciler.DryRun(t.Context())
		_, dbErr := os.Stat(filepath.Join(dir, "control.db"))
		t.Fatalf("missing %s became successful preview: error=%v state=%s report-error=%v real DB exists=%t", name, err, report.ControlState, reportErr, dbErr == nil)
	}
}

func closeAdmissionPreview(t *testing.T, runtime *Runtime) {
	t.Helper()
	if runtime == nil {
		return
	}
	if err := runtime.Close(); err != nil {
		t.Error(err)
	}
}
