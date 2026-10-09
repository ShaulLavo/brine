package plan

import (
	"errors"
	"github.com/ShaulLavo/brine/internal/target"
	"testing"
)

func TestRemovePlanOwnershipAndNoOp(t *testing.T) {
	in := installed(t)
	in.State.Releases[0].Units = in.State.Releases[0].Units[:1]
	(*in.Snapshot.Apps.Value)[0].QuadletUnits = target.Known(in.State.Releases[0].Units)
	p, err := BuildRemove(in)
	if err != nil || p.Kind != Update || p.Lifecycle != RemoveApp || p.Removal == nil || p.Removal.ReleaseID != in.State.Releases[0].ID {
		t.Fatalf("%+v %v", p, err)
	}
	if len(p.Changes) != 4 {
		t.Fatalf("removal changes %+v", p.Changes)
	}
	in.Snapshot.CaddyConfig.Value.Files[0].Hash = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	drift, err := BuildRemove(in)
	if err != nil || drift.Kind != Conflict || len(drift.Changes) != 0 {
		t.Fatalf("drift %+v %v", drift, err)
	}
	in = fixture(t, "ready-arm64")
	p, err = BuildRemove(in)
	if err != nil || p.Kind != NoOp || len(p.Changes) != 0 {
		t.Fatalf("absent %+v %v", p, err)
	}
	in.State.Releases = []CurrentRelease{}
	in.Snapshot.Apps = target.Known([]target.App{{Name: "hello", Image: target.Observation[target.Image]{Status: target.Unknown}, AllocatedHostPort: target.Observation[target.Port]{Status: target.Unknown}, QuadletUnits: target.Known([]target.Unit{}), Secrets: target.Known([]target.Secret{})}})
	p, err = BuildRemove(in)
	if err != nil || p.Kind != Conflict {
		t.Fatalf("unowned %+v %v", p, err)
	}
}
func TestRemovePersistentDataRefused(t *testing.T) {
	in := installed(t)
	if _, err := BuildRemove(in); !errors.Is(err, ErrPersistentData) {
		t.Fatalf("expected archive refusal, got %v", err)
	}
}
