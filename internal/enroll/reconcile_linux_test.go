//go:build linux

package enroll

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestBootReconcileUnitAndEnrollmentUndo(t *testing.T) {
	for _, want := range []string{"ConditionUser=brine", "Type=oneshot", "ExecStart=/usr/local/bin/brine host reconcile --json", "TimeoutStartSec=15min", "Restart=no"} {
		if !strings.Contains(reconcileUnit, want) {
			t.Fatalf("missing %s", want)
		}
	}
	steps := (&host{}).steps()
	found := false
	for _, step := range steps {
		if step.Name == "boot-reconcile" {
			found = true
			if step.Check == nil || step.Apply == nil || step.Undo == nil {
				t.Fatal("boot unit not transactional")
			}
		}
	}
	if !found {
		t.Fatal("boot unit missing from enrollment transaction")
	}
}

func TestBootGeneratorActivatesOnlyBrine(t *testing.T) {
	for _, user := range []string{"brine", "unrelated"} {
		t.Run(user, func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "output with spaces $literal")
			if err := os.Mkdir(dir, 0700); err != nil {
				t.Fatal(err)
			}
			// Substitute only the identity lookup; run the installed generator body
			// against disposable output, never systemd directories or a real account.
			script := strings.Replace(reconcileGenerator, "/usr/bin/id -un", "/usr/bin/printf "+user, 1)
			cmd := exec.Command("/bin/sh", "-c", script, "generator", dir, dir, dir)
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("generator %v %s", err, out)
			}
			link := filepath.Join(dir, "default.target.wants/brine-reconcile.service")
			target, err := os.Readlink(link)
			if user == "brine" {
				if err != nil || target != reconcileUnitPath {
					t.Fatalf("activation %q %v", target, err)
				}
			} else if !os.IsNotExist(err) {
				t.Fatal("changed another user's target")
			}
		})
	}
}
