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

func TestRemoveRetainedResourcesAreNotAnOwnedDeployment(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*target.App)
		kind   Kind
	}{
		{"retained secret", func(*target.App) {}, NoOp},
		{"unknown image", func(a *target.App) { a.Image.Status = target.Unknown }, Conflict},
		{"unknown port", func(a *target.App) { a.AllocatedHostPort.Status = target.Unknown }, Conflict},
		{"unknown units", func(a *target.App) { a.QuadletUnits = target.Observation[[]target.Unit]{Status: target.Unknown} }, Conflict},
		{"remaining unit", func(a *target.App) {
			a.QuadletUnits = target.Known([]target.Unit{{Name: "hello.container", Hash: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}})
		}, Conflict},
		{"unreadable secrets", func(a *target.App) { a.Secrets = target.Observation[[]target.Secret]{Status: target.Unknown} }, Conflict},
	} {
		t.Run(tc.name, func(t *testing.T) {
			in := fixture(t, "ready-arm64")
			app := target.App{Name: "hello", Image: target.Observation[target.Image]{Status: target.Absent}, AllocatedHostPort: target.Observation[target.Port]{Status: target.Absent}, QuadletUnits: target.Known([]target.Unit{}), Secrets: target.Known([]target.Secret{{Name: "brine.hello.db.v1", ID: "retained"}})}
			tc.change(&app)
			in.Snapshot.Apps = target.Known([]target.App{app})
			p, err := BuildRemove(in)
			if err != nil || p.Kind != tc.kind {
				t.Fatal(p.Conflicts, err)
			}
		})
	}
}
