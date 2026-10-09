package plan

import (
	"reflect"
	"testing"

	"github.com/ShaulLavo/brine/internal/target"
)

func TestFreeDiskDecision(t *testing.T) {
	const minimum uint64 = 1 << 30
	in := fixture(t, "ready-arm64")
	in.Snapshot.FreeDiskBytes = target.Known(minimum)
	baseline := build(t, in)
	if baseline.Kind != Create {
		t.Fatal(baseline)
	}
	for _, free := range []uint64{minimum + 1, minimum + 4096, minimum * 2, ^uint64(0)} {
		in.Snapshot.FreeDiskBytes = target.Known(free)
		next := build(t, in)
		if !reflect.DeepEqual(baseline, next) {
			t.Fatalf("free disk %d changed a sufficient-disk plan", free)
		}
	}
	in.Snapshot.FreeDiskBytes = target.Known(minimum - 1)
	refused := build(t, in)
	if refused.Kind != Conflict || len(refused.Changes) != 0 || refused.Hash == baseline.Hash {
		t.Fatal("insufficient disk did not change the plan into a conflict", refused)
	}
	if !reflect.DeepEqual(refused.Conflicts, []Diagnostic{{Code: "insufficient_disk", Field: "free_disk_bytes"}}) {
		t.Fatal(refused.Conflicts)
	}
	in.Snapshot.FreeDiskBytes = target.Known(uint64(0))
	if next := build(t, in); !reflect.DeepEqual(refused, next) {
		t.Fatal("insufficient measurements must have the same decision", next)
	}
}

func TestUnobservedDiskConflicts(t *testing.T) {
	for _, status := range []target.Status{target.Unknown, target.Unsupported} {
		t.Run(string(status), func(t *testing.T) {
			in := fixture(t, "ready-arm64")
			in.Snapshot.FreeDiskBytes = target.Observation[uint64]{Status: status}
			p := build(t, in)
			code := UnknownFacts
			if status == target.Unsupported {
				code = UnsupportedTarget
			}
			if p.Kind != Conflict || len(p.Changes) != 0 || !reflect.DeepEqual(p.Conflicts, []Diagnostic{{Code: code, Field: "free_disk_bytes"}}) {
				t.Fatal(p)
			}
		})
	}
}
