package plan

import (
	"strings"
	"testing"

	"github.com/ShaulLavo/brine/internal/target"
)

func TestUnrelatedAppImageDoesNotChangePlan(t *testing.T) {
	for _, name := range []string{"aaa", "zzz"} {
		for _, status := range []target.Status{target.KnownStatus, target.Unknown, target.Absent, target.Unsupported} {
			t.Run(name+"/"+string(status), func(t *testing.T) {
				in := installed(t)
				other := (*in.Snapshot.Apps.Value)[0]
				other.Name = name
				other.AllocatedHostPort = target.Known(target.Port(20001))
				other.QuadletUnits = target.Known([]target.Unit{})
				other.Secrets = target.Known([]target.Secret{})
				in.Snapshot.Apps = target.Known(append(*in.Snapshot.Apps.Value, other))
				before := build(t, in)
				for i := range *in.Snapshot.Apps.Value {
					a := &(*in.Snapshot.Apps.Value)[i]
					if a.Name != name {
						continue
					}
					a.Image = target.Observation[target.Image]{Status: status}
					if status == target.KnownStatus {
						a.Image = target.Known(target.Image{Digest: "sha256:" + strings.Repeat("f", 64), Platform: target.Platform{OS: "linux", Arch: "arm64"}})
					}
					inactive := target.Known(false)
					a.UnitActive = &inactive
				}
				after := build(t, in)
				if before.Hash != after.Hash {
					t.Fatalf("unrelated image changed hash: %s -> %s", before.Hash, after.Hash)
				}
			})
		}
	}
}

func TestRemoveBindsTargetImageAndOwnedUnit(t *testing.T) {
	in := installed(t)
	in.State.Releases[0].Units = in.State.Releases[0].Units[:1]
	(*in.Snapshot.Apps.Value)[0].QuadletUnits = target.Known(in.State.Releases[0].Units)
	before, err := BuildRemove(in)
	if err != nil || before.Kind != Update {
		t.Fatal(before, err)
	}
	// Target observation changes remain freshness preconditions.
	(*in.Snapshot.Apps.Value)[0].Image = target.Observation[target.Image]{Status: target.Unknown}
	after, err := BuildRemove(in)
	if err != nil || before.Hash == after.Hash {
		t.Fatalf("target image change kept removal hash: %s -> %s (%v)", before.Hash, after.Hash, err)
	}
	(*in.Snapshot.Apps.Value)[0].QuadletUnits = target.Known([]target.Unit{{Name: "hello.container", Hash: "sha256:" + strings.Repeat("f", 64)}})
	drift, err := BuildRemove(in)
	if err != nil || drift.Kind != Conflict || drift.Hash == before.Hash {
		t.Fatal("owned image/unit drift was not refused", drift, err)
	}
}

func TestTargetAppImageStillChangesPlan(t *testing.T) {
	for _, status := range []target.Status{target.KnownStatus, target.Unknown} {
		t.Run(string(status), func(t *testing.T) {
			in := installed(t)
			before := build(t, in)
			a := &(*in.Snapshot.Apps.Value)[0]
			if status == target.KnownStatus {
				a.Image.Value.Digest = "sha256:" + strings.Repeat("f", 64)
			} else {
				a.Image = target.Observation[target.Image]{Status: status}
			}
			after := build(t, in)
			if before.Hash == after.Hash {
				t.Fatal("target image drift kept hash")
			}
		})
	}
}
