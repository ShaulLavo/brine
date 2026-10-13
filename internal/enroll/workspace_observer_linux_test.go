//go:build linux

package enroll

import (
	"strings"
	"testing"

	"github.com/ShaulLavo/brine/internal/workspaceobserve"
)

func TestWorkspaceObserverEnrollmentRequiresActualServiceReadiness(t *testing.T) {
	exact := "LoadState=loaded\nActiveState=active\nUnitFileState=enabled\nFragmentPath=" + workspaceobserve.UnitPath + "\n"
	if !observerInstalled(exact) {
		t.Fatal("exact service refused")
	}
	for _, raw := range []string{strings.Replace(exact, "active", "inactive", 1), strings.Replace(exact, "enabled", "disabled", 1), strings.Replace(exact, workspaceobserve.UnitPath, "/foreign", 1), exact + "ActiveState=active\n", strings.Replace(exact, "LoadState=loaded\n", "", 1)} {
		if observerInstalled(raw) {
			t.Fatal("partial or foreign service claimed installed")
		}
	}
	allowed, err := workspaceobserve.ParseGrant([]byte(workspaceobserve.DefaultGrant))
	if err != nil || allowed {
		t.Fatal("new enrollment grants privileged observation", err)
	}
	if !strings.Contains(workspaceobserve.Unit, "Group=brine\n") || strings.Contains(workspaceobserve.Unit, "CAP_CHOWN") || strings.Contains(workspaceobserve.Unit, "ProtectSystem") || strings.Contains(workspaceobserve.Unit, "PrivateMounts") || strings.Contains(workspaceobserve.Unit, "ProtectProc") || strings.Contains(workspaceobserve.Unit, "ProcSubset") {
		t.Fatal("observer sandbox breaks declared authority/mount/proc boundary")
	}
}
