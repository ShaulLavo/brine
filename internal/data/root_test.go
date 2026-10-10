package data

import (
	"testing"
	"time"
)

func TestRootEvidenceRequiresAffirmativeAdmission(t *testing.T) {
	good := RootEvidence{Root: "/srv/data", Device: 1, Inode: 2, Filesystem: "ext4", POSIXLocks: true, DurableRename: true, FreeBytes: 1024, FreeInodes: 1, ObservedAt: time.Now()}
	if !good.Admits("/srv/data", 1024) {
		t.Fatal("complete evidence rejected")
	}
	for _, change := range []func(*RootEvidence){
		func(e *RootEvidence) { e.Root = "/srv/other" }, func(e *RootEvidence) { e.Device = 0 }, func(e *RootEvidence) { e.Inode = 0 },
		func(e *RootEvidence) { e.Filesystem = "nfs" }, func(e *RootEvidence) { e.POSIXLocks = false }, func(e *RootEvidence) { e.DurableRename = false },
		func(e *RootEvidence) { e.FreeBytes = 1023 }, func(e *RootEvidence) { e.FreeInodes = 0 }, func(e *RootEvidence) { e.ObservedAt = time.Time{} },
	} {
		e := good
		change(&e)
		if e.Admits("/srv/data", 1024) {
			t.Fatalf("incomplete evidence admitted: %+v", e)
		}
	}
}
func TestOverlappingPaths(t *testing.T) {
	for _, tc := range []struct {
		a, b string
		want bool
	}{
		{"/srv/data", "/srv/data", true}, {"/srv/data/apps", "/srv/data", true}, {"/srv/data", "/srv/data/apps", true},
		{"/srv/data-other", "/srv/data", false}, {"/srv/a", "/srv/b", false}, {"/", "/srv/data", true},
	} {
		if got := OverlappingPaths(tc.a, tc.b); got != tc.want {
			t.Errorf("overlap(%q,%q)=%v", tc.a, tc.b, got)
		}
	}
}
