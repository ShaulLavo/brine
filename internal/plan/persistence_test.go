package plan

import (
	"testing"
	"time"

	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/target"
)

func persistentReady(t *testing.T) Input {
	t.Helper()
	in := fixture(t, "ready-arm64")
	now := time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)
	runtime := data.RuntimeIdentity{UID: 10001, GID: 10001}
	cadence := data.DefaultBackupCadence()
	declaration := data.Database{Name: "main", PersistentRoot: "/srv/data", MountPath: "/data", Filename: "app.db", BackupDestination: "primary", SyncInterval: time.Minute}
	destination := data.Destination{Reference: "primary", Endpoint: "https://storage.example", Region: "region-1", Bucket: "backups", BasePrefix: "brine", CredentialRef: "primary"}
	in.Desired.Runtime = &runtime
	in.Desired.Backup = &cadence
	in.Desired.Databases = []data.Database{declaration}
	in.Desired.PersistentRoots = []data.PersistentRoot{declaration.PersistentRoot}
	in.Desired.BackupDestinations = []data.Destination{destination}
	in.Desired.SchemaCompatibility = []data.SchemaCompatibility{{Database: "main", Startup: "preserve", Accepts: []string{data.EmptyMarker}}}
	incarnation := data.AppIncarnationID("11111111111111111111111111111111")
	database := data.DatabaseID("22222222222222222222222222222222")
	relative, _ := data.RelativeDirectory(incarnation, database)
	binding := data.DatabaseBinding{DatabaseID: database, IncarnationID: incarnation, Name: "main", Root: declaration.PersistentRoot, RelativeDirectory: relative, MountPath: declaration.MountPath, Filename: declaration.Filename, ReplicaBindingID: "33333333333333333333333333333333"}
	root := data.RootEvidence{Root: binding.Root, Device: 1, Inode: 2, Filesystem: "ext4", POSIXLocks: true, DurableRename: true, FreeBytes: in.Desired.MinimumFreeDiskBytes * 2, FreeInodes: 10, ObservedAt: now}
	retention := data.RetentionEvidence{Destination: "primary", Endpoint: destination.Endpoint, Bucket: destination.Bucket, BasePrefix: destination.BasePrefix, VerificationID: "44444444444444444444444444444444", VerifiedAt: now.Add(-time.Hour).Format(time.RFC3339), FreshnessSeconds: 24 * 60 * 60, NoObjectExpiration: true}
	in.Desired.BackupRetention = []data.RetentionEvidence{retention}
	mapping := data.MappingEvidence{Runtime: runtime, RunnerUID: 1000, RunnerGID: 1000, Root: root.Root, Device: root.Device, Image: string(in.Desired.Image), KeepID: true, PrivateModes: true, HostReadWrite: true, ContainerReadWrite: true, ObservedAt: now}
	facts := target.Known([]target.PersistentDatabase{{Definitions: []data.SchemaDefinition{{Database: "main", Marker: data.EmptyMarker, CatalogSHA256: data.EmptyCatalogSHA256}}, Database: binding, Root: root, Mapping: target.Known(mapping), Retention: target.Known(retention), Schema: data.SchemaObservation{DatabaseID: database, State: data.AllocatedEmpty, Marker: data.EmptyMarker, CatalogSHA256: data.EmptyCatalogSHA256, ObservedAt: now}}})
	in.Snapshot.PersistentData = &facts
	return in
}
func TestPersistentPlanRequiresCompleteMeasuredEvidence(t *testing.T) {
	in := persistentReady(t)
	p := build(t, in)
	if p.Kind != Create || len(p.DataMounts) != 1 {
		t.Fatalf("persistent fixture not plannable: %+v", p.Conflicts)
	}
	rendered := false
	for _, change := range p.Changes {
		if change.Quadlet != nil {
			rendered = len(change.Quadlet.DataMounts) == 1
		}
	}
	if !rendered {
		t.Fatal("persistent mounts omitted from render change")
	}
	for _, tc := range []struct {
		name   string
		change func(*target.PersistentDatabase)
		want   ConflictCode
	}{
		{"unknown schema", func(f *target.PersistentDatabase) {
			f.Schema.State = data.Unknown
			f.Schema.Marker = ""
			f.Schema.CatalogSHA256 = ""
			f.Schema.UnknownReason = "missing"
		}, SchemaStateUnknown},
		{"wrong schema", func(f *target.PersistentDatabase) { f.Schema.State = data.VerifiedSchema; f.Schema.Marker = "v2" }, SchemaIncompatible},
		{"held fence", func(f *target.PersistentDatabase) { f.Fenced = true }, DataFenced},
		{"missing mapping", func(f *target.PersistentDatabase) {
			f.Mapping = target.Observation[data.MappingEvidence]{Status: target.Unknown}
		}, DataMappingUnknown},
		{"expired retention", func(f *target.PersistentDatabase) {
			f.Retention.Value.VerifiedAt = f.Schema.ObservedAt.Add(-48 * time.Hour).Format(time.RFC3339)
		}, BackupRetentionUnknown},
		{"expiring objects", func(f *target.PersistentDatabase) { f.Retention.Value.NoObjectExpiration = false }, BackupRetentionUnknown},
		{"space exhausted", func(f *target.PersistentDatabase) { f.Root.FreeBytes = 0 }, InsufficientDisk},
	} {
		t.Run(tc.name, func(t *testing.T) {
			in := persistentReady(t)
			tc.change(&(*in.Snapshot.PersistentData.Value)[0])
			p := build(t, in)
			found := false
			for _, d := range p.Conflicts {
				if d.Code == tc.want {
					found = true
				}
			}
			if !found || p.Kind != Conflict || len(p.Changes) != 0 {
				t.Fatalf("unsafe persistent plan %+v", p.Conflicts)
			}
		})
	}
}
func TestPersistentDecisionIgnoresHealthyClockAndCapacityMovement(t *testing.T) {
	in := persistentReady(t)
	original := build(t, in)
	fact := &(*in.Snapshot.PersistentData.Value)[0]
	fact.Schema.ObservedAt = fact.Schema.ObservedAt.Add(time.Minute)
	fact.Root.ObservedAt = fact.Root.ObservedAt.Add(time.Minute)
	fact.Mapping.Value.ObservedAt = fact.Mapping.Value.ObservedAt.Add(time.Minute)
	fact.Root.FreeBytes++
	fact.Root.FreeInodes++
	fresh := build(t, in)
	if fresh.Hash != original.Hash {
		t.Fatal("healthy observation/capacity movement invalidated plan")
	}
}

func TestPersistentPlanRejectsIncompleteCompatibility(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*Input)
	}{
		{"unregistered marker", func(in *Input) {
			in.Desired.SchemaCompatibility[0].Accepts = append(in.Desired.SchemaCompatibility[0].Accepts, "v2")
		}},
		{"duplicate set", func(in *Input) {
			in.Desired.SchemaCompatibility = append(in.Desired.SchemaCompatibility, in.Desired.SchemaCompatibility[0])
		}},
		{"unmatched set", func(in *Input) {
			in.Desired.SchemaCompatibility = append(in.Desired.SchemaCompatibility, data.SchemaCompatibility{Database: "other", Startup: "preserve", Accepts: []string{data.EmptyMarker}})
		}},
		{"wrong fingerprint", func(in *Input) {
			(*in.Snapshot.PersistentData.Value)[0].Schema.CatalogSHA256 = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			in := persistentReady(t)
			tc.change(&in)
			p := build(t, in)
			if p.Kind != Conflict {
				t.Fatal("incomplete D12 compatibility admitted")
			}
		})
	}
}
