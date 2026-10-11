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

func TestBtrfsAdmissionHasDynamicInodes(t *testing.T) {
	e := RootEvidence{Root: "/srv/data", Device: 1, Inode: 2, Filesystem: "btrfs", POSIXLocks: true, DurableRename: true, FreeBytes: 1024, FreeInodes: 0, ObservedAt: time.Now()}
	if !e.Admits("/srv/data", 1024) {
		t.Fatal("Btrfs dynamic inodes refused")
	}
	for _, fs := range []string{"ext4", "xfs"} {
		e.Filesystem = fs
		if e.Admits("/srv/data", 1024) {
			t.Fatal("fixed-inode filesystem exhaustion accepted")
		}
	}
}

func TestRootPreparationProofIsIdentityBoundAndKeepsFreshCapacity(t *testing.T) {
	proof := RootEvidence{Root: "/srv/data", Device: 1, Inode: 2, Filesystem: "ext4", POSIXLocks: true, DurableRename: true, FreeBytes: 1024, FreeInodes: 10, ObservedAt: time.Now().Add(-time.Hour)}
	inspected := proof
	inspected.POSIXLocks, inspected.DurableRename = false, false
	inspected.FreeBytes, inspected.FreeInodes = 512, 1
	inspected.ObservedAt = time.Now()
	merged, err := inspected.WithPreparationProof(&proof)
	if err != nil || !merged.Admits(proof.Root, 512) || merged.Admits(proof.Root, 513) || merged.ObservedAt != inspected.ObservedAt || merged.FreeInodes != 1 {
		t.Fatal("proof overwrote fresh inspection", merged, err)
	}
	if _, err := inspected.WithPreparationProof(nil); err == nil {
		t.Fatal("missing proof admitted")
	}
	for _, change := range []func(*RootEvidence){
		func(e *RootEvidence) { e.Root = "/srv/other" }, func(e *RootEvidence) { e.Device++ }, func(e *RootEvidence) { e.Inode++ },
		func(e *RootEvidence) { e.Filesystem = "xfs" }, func(e *RootEvidence) { e.POSIXLocks = false }, func(e *RootEvidence) { e.DurableRename = false },
	} {
		changed := proof
		change(&changed)
		if _, err := inspected.WithPreparationProof(&changed); err == nil {
			t.Fatal("changed or incomplete proof admitted", changed)
		}
	}
}
