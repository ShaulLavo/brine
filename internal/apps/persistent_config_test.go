package apps

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/plan"
	"github.com/ShaulLavo/brine/internal/policy"
	"github.com/ShaulLavo/brine/internal/target"
)

type persistentFactsFunc func(context.Context, policy.Desired) (target.Observation[[]target.PersistentDatabase], error)

func (f persistentFactsFunc) Collect(ctx context.Context, d policy.Desired) (target.Observation[[]target.PersistentDatabase], error) {
	return f(ctx, d)
}

func persistentConfigFixture(t *testing.T) (Service, *fakeStore, target.Observation[[]target.PersistentDatabase]) {
	t.Helper()
	s, db := configFixture(t)
	raw, err := os.ReadFile("../policy/testdata/operator.toml")
	if err != nil {
		t.Fatal(err)
	}
	raw = []byte(strings.ReplaceAll(string(raw), "Registry.Example.com:5000", "ghcr.io") + `
[[backup_destinations]]
reference="primary"
endpoint="https://storage.example"
region="region-1"
bucket="backups"
base_prefix="brine"
credential_ref="primary"
[[backup_retention]]
destination="primary"
endpoint="https://storage.example"
bucket="backups"
base_prefix="brine"
verification_id="44444444444444444444444444444444"
verified_at="2026-10-10T11:00:00Z"
freshness_seconds=86400
no_object_expiration=true
`)
	pol, err := policy.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	s.LoadPolicy = func(context.Context) (policy.Policy, error) { return pol, nil }
	app := db.desired["current-plan"].App()
	app.Runtime = &data.RuntimeIdentity{UID: 10001, GID: 10001}
	app.Databases = []data.Database{{Name: "main", PersistentRoot: "/srv/brine/data", MountPath: "/data", Filename: "app.db", BackupDestination: "primary", SyncInterval: time.Minute}}
	app.SchemaCompatibility = []data.SchemaCompatibility{{Database: "main", Startup: "preserve", Accepts: []string{data.EmptyMarker}}}
	desired, err := policy.Normalize(app, pol)
	if err != nil {
		t.Fatal(err)
	}
	db.desired["current-plan"] = desired
	db.state.Releases[0].Desired = desired
	now := time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)
	incarnation := data.AppIncarnationID(strings.Repeat("1", 32))
	database := data.DatabaseID(strings.Repeat("2", 32))
	bindingID := data.ReplicaBindingID(strings.Repeat("3", 32))
	relative, _ := data.RelativeDirectory(incarnation, database)
	binding := data.DatabaseBinding{DatabaseID: database, Name: "main", IncarnationID: incarnation, Root: app.Databases[0].PersistentRoot, RelativeDirectory: relative, MountPath: "/data", Filename: "app.db", ReplicaBindingID: bindingID}
	root := data.RootEvidence{Root: binding.Root, Device: 1, Inode: 2, Filesystem: "ext4", POSIXLocks: true, DurableRename: true, FreeBytes: desired.MinimumFreeDiskBytes * 2, FreeInodes: 10, ObservedAt: now}
	facts := target.Known([]target.PersistentDatabase{{
		Database: binding, Root: root,
		Mapping:     target.Known(data.MappingEvidence{Runtime: *desired.Runtime, RunnerUID: 1000, RunnerGID: 1000, Root: root.Root, Device: root.Device, Image: string(desired.Image), KeepID: true, PrivateModes: true, HostReadWrite: true, ContainerReadWrite: true, ObservedAt: now}),
		Retention:   target.Known(desired.BackupRetention[0]),
		Credentials: target.Known(data.CredentialEvidence{BindingID: bindingID, EpochID: data.ReplicaEpochID(strings.Repeat("5", 32)), Destination: "primary", Reference: "primary", Version: 1, PolicyHash: desired.PolicyHash, ReceivedAt: now.Add(-time.Hour)}),
		Usage:       target.Known(data.StorageUsage{}),
		Definitions: []data.SchemaDefinition{{Database: "main", Marker: data.EmptyMarker, CatalogSHA256: data.EmptyCatalogSHA256}},
		Schema:      data.SchemaObservation{DatabaseID: database, State: data.AllocatedEmpty, Marker: data.EmptyMarker, CatalogSHA256: data.EmptyCatalogSHA256, ObservedAt: now},
	}})
	return s, db, facts
}

func TestPersistentConfigAndLifecycleCollectFreshFacts(t *testing.T) {
	for _, action := range []string{"config", "restart", "stop", "start"} {
		t.Run(action, func(t *testing.T) {
			s, db, facts := persistentConfigFixture(t)
			calls := 0
			s.Data = persistentFactsFunc(func(_ context.Context, d policy.Desired) (target.Observation[[]target.PersistentDatabase], error) {
				calls++
				if len(d.Databases) != 1 || len(d.BackupDestinations) != 1 || d.PolicyHash != db.desired["current-plan"].PolicyHash {
					t.Fatal("collector received incomplete desired scope")
				}
				if action == "config" && (len(d.Environment) != 1 || d.Environment[0].Value != "two") {
					t.Fatal("facts collected before edits")
				}
				return facts, nil
			})
			var got ConfigPlan
			var err error
			if action == "config" {
				got, err = s.ConfigSet(context.Background(), "hello", []Edit{{Key: "environment.RELEASE", Value: "two"}})
			} else {
				got, err = s.Lifecycle(context.Background(), "hello", plan.ChangeKind(action+"_app"))
			}
			if err != nil {
				t.Fatal(err)
			}
			if calls != 1 || got.Kind != plan.Update {
				t.Fatalf("calls=%d plan=%+v", calls, got)
			}
			if len(db.saved) != 1 || len(db.saved[0].DataMounts) != 1 {
				t.Fatal("persistent mount scope lost")
			}
		})
	}
}

func TestPersistentConfigFactsFailureDoesNotSavePlan(t *testing.T) {
	s, db, _ := persistentConfigFixture(t)
	failure := errors.New("fixture collection failed")
	s.Data = persistentFactsFunc(func(context.Context, policy.Desired) (target.Observation[[]target.PersistentDatabase], error) {
		return target.Observation[[]target.PersistentDatabase]{}, failure
	})
	_, err := s.ConfigSet(context.Background(), "hello", []Edit{{Key: "environment.RELEASE", Value: "two"}})
	if !errors.Is(err, failure) || len(db.saved) != 0 {
		t.Fatal("collection failure saved plan", err, len(db.saved))
	}
}

func TestPersistentConfigWithoutFactsFailsClosed(t *testing.T) {
	s, _, _ := persistentConfigFixture(t)
	got, err := s.ConfigSet(context.Background(), "hello", []Edit{{Key: "environment.RELEASE", Value: "two"}})
	if err != nil || got.Kind != plan.Conflict {
		t.Fatal("missing collector admitted update", got, err)
	}
	found := false
	for _, conflict := range got.Conflicts {
		if conflict.Code == plan.UnknownFacts && conflict.Field == "databases" {
			found = true
		}
	}
	if !found {
		t.Fatal("missing database conflict", got)
	}
}

func TestPersistentConfigFreshRefusalsOverrideInventory(t *testing.T) {
	for _, name := range []string{"unknown", "fenced"} {
		t.Run(name, func(t *testing.T) {
			s, _, facts := persistentConfigFixture(t)
			snapshot := s.Inventory.(inventory).snapshot
			snapshot.PersistentData = &facts
			s.Inventory = inventory{snapshot}
			s.Data = persistentFactsFunc(func(context.Context, policy.Desired) (target.Observation[[]target.PersistentDatabase], error) {
				if name == "unknown" {
					return target.Observation[[]target.PersistentDatabase]{Status: target.Unknown}, nil
				}
				fresh := *facts.Value
				fresh[0].Fenced = true
				return target.Known(fresh), nil
			})
			got, err := s.ConfigSet(context.Background(), "hello", []Edit{{Key: "environment.RELEASE", Value: "two"}})
			if err != nil || got.Kind != plan.Conflict {
				t.Fatal("fresh unsafe facts admitted update", got, err)
			}
			want := plan.UnknownFacts
			if name == "fenced" {
				want = plan.DataFenced
			}
			found := false
			for _, conflict := range got.Conflicts {
				if conflict.Code == want {
					found = true
				}
			}
			if !found {
				t.Fatal("missing fresh refusal", got)
			}
		})
	}
}

func TestStatelessConfigDoesNotCollectPersistentFacts(t *testing.T) {
	s, _ := configFixture(t)
	s.Data = persistentFactsFunc(func(context.Context, policy.Desired) (target.Observation[[]target.PersistentDatabase], error) {
		t.Fatal("collected data for stateless app")
		return target.Observation[[]target.PersistentDatabase]{}, nil
	})
	got, err := s.ConfigSet(context.Background(), "hello", []Edit{{Key: "environment.RELEASE", Value: "two"}})
	if err != nil || got.Kind != plan.Update {
		t.Fatal(got, err)
	}
}
