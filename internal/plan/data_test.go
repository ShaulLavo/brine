package plan

import (
	"errors"
	"testing"
	"time"

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

func TestPersistentAdmissionDiagnostics(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*Input)
		want   ConflictCode
	}{
		{"missing compatibility", func(in *Input) { in.Desired.SchemaCompatibility = nil }, SchemaCompatibilityRequired},
		{"cadence below bounds", func(in *Input) { in.Desired.Databases[0].SyncInterval = time.Second }, BackupCadenceDenied},
		{"cadence above bounds", func(in *Input) { in.Desired.Databases[0].SyncInterval = 2 * time.Hour }, BackupCadenceDenied},
		{"root removed", func(in *Input) { in.Desired.PersistentRoots = nil }, PersistentRootDenied},
		{"destination missing", func(in *Input) { in.Desired.BackupDestinations = nil }, BackupDestinationUnknown},
	} {
		t.Run(tc.name, func(t *testing.T) {
			in := fixture(t, "ready-arm64")
			cadence := data.DefaultBackupCadence()
			in.Desired.Runtime = &data.RuntimeIdentity{UID: 10001, GID: 10001}
			in.Desired.Backup = &cadence
			in.Desired.Databases = []data.Database{{Name: "main", PersistentRoot: "/srv/data", MountPath: "/data", Filename: "app.db", BackupDestination: "primary", SyncInterval: time.Minute}}
			in.Desired.PersistentRoots = []data.PersistentRoot{"/srv/data"}
			in.Desired.BackupDestinations = []data.Destination{{Reference: "primary"}}
			in.Desired.SchemaCompatibility = []data.SchemaCompatibility{{Database: "main", Startup: "preserve", Accepts: []string{data.EmptyMarker}}}
			tc.change(&in)
			p := build(t, in)
			found := false
			for _, d := range p.Conflicts {
				if d.Code == tc.want {
					found = true
				}
			}
			if !found || p.Kind != Conflict || len(p.Changes) != 0 {
				t.Fatalf("admission not refused: %+v", p)
			}
			if p.Backup == nil || p.Backup.SnapshotInterval != 6*time.Hour || p.Runtime == nil {
				t.Fatal("effective data render inputs omitted")
			}
		})
	}
}
