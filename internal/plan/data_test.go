package plan

import (
	"errors"
	"testing"

	"github.com/ShaulLavo/brine/internal/data"
)

func TestPersistentCheckpointCannotDeploy(t *testing.T) {
	in := fixture(t, "ready-arm64")
	in.Desired.Runtime = &data.RuntimeIdentity{UID: 10001, GID: 10001}
	in.Desired.Databases = []data.Database{{Name: "main", PersistentRoot: "/srv/data", MountPath: "/data", Filename: "app.db", BackupDestination: "primary"}}
	p := build(t, in)
	if p.Kind != Conflict || len(p.Changes) != 0 {
		t.Fatal("checkpoint enabled unverified persistent deployment")
	}
	found := false
	for _, d := range p.Conflicts {
		if d.Code == UnknownFacts && d.Field == "databases" {
			found = true
		}
	}
	if !found {
		t.Fatal("missing database evidence conflict")
	}
}

func TestPersistentCheckpointCannotRemove(t *testing.T) {
	in := installed(t)
	in.State.Releases[0].Desired.Databases = []data.Database{{Name: "main"}}
	if _, err := BuildRemove(in); !errors.Is(err, ErrPersistentData) {
		t.Fatalf("stored persistent app removed: %v", err)
	}
	in.State.Releases[0].Desired.Databases = nil
	in.Desired.Runtime = &data.RuntimeIdentity{UID: 10001, GID: 10001}
	if _, err := BuildRemove(in); !errors.Is(err, ErrPersistentData) {
		t.Fatalf("persistent desired removed: %v", err)
	}
}
