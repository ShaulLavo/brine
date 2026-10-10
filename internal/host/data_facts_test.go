//go:build linux

package host

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/localexec"
	"github.com/ShaulLavo/brine/internal/ops"
	"github.com/ShaulLavo/brine/internal/plan"
	"github.com/ShaulLavo/brine/internal/policy"
	"github.com/ShaulLavo/brine/internal/spec"
	"github.com/ShaulLavo/brine/internal/store"
	"github.com/ShaulLavo/brine/internal/target"
)

func persistentHostFixture(t *testing.T) (*store.Store, policy.Desired, DataFacts) {
	t.Helper()
	s, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	root := t.TempDir()
	if err = os.Chmod(root, 0700); err != nil { //nolint:gosec // Private directory requires execute permission for traversal.
		t.Fatal(err)
	}
	if _, err = data.InspectRoot(root); err != nil {
		t.Skipf("private local filesystem unavailable: %v", err)
	}
	now := time.Now().UTC()
	cadence := data.DefaultBackupCadence()
	destination := data.Destination{Reference: "primary", Endpoint: "https://storage.example", Region: "region-1", Bucket: "backups", BasePrefix: "brine", CredentialRef: "primary"}
	d := policy.Desired{SchemaVersion: 1, AppPorts: policy.PortRange{Min: 20000, Max: 20100}, PolicyVersion: "fixture", Image: spec.ImageReference("ghcr.io/team/hello@sha256:" + strings.Repeat("a", 64)), Name: "hello", PolicyHash: "sha256:" + strings.Repeat("a", 64), Runtime: &data.RuntimeIdentity{UID: 10001, GID: 10001}, Backup: &cadence, Databases: []data.Database{{Name: "main", PersistentRoot: data.PersistentRoot(root), MountPath: "/data", Filename: "app.db", BackupDestination: "primary", SyncInterval: time.Minute}}, PersistentRoots: []data.PersistentRoot{data.PersistentRoot(root)}, BackupDestinations: []data.Destination{destination}, SchemaCompatibility: []data.SchemaCompatibility{{Database: "main", Startup: "preserve", Accepts: []string{data.EmptyMarker}}}, BackupRetention: []data.RetentionEvidence{{Destination: "primary", Endpoint: destination.Endpoint, Bucket: destination.Bucket, BasePrefix: destination.BasePrefix, VerificationID: strings.Repeat("b", 32), VerifiedAt: now.Add(-time.Minute).Format(time.RFC3339Nano), FreshnessSeconds: 3600, NoObjectExpiration: true}}}
	f := DataFacts{Store: s, ProbeRoot: func(ctx context.Context, path string) (data.RootEvidence, error) { return data.ProbeRoot(ctx, path) }, ProbeMapping: func(_ context.Context, _ localexec.Runner, r data.RootEvidence, identity data.RuntimeIdentity) (data.MappingEvidence, error) {
		return data.MappingEvidence{Runtime: identity, Root: r.Root, Device: r.Device, RunnerUID: 1000, RunnerGID: 1000, Image: "probe", KeepID: true, PrivateModes: true, HostReadWrite: true, ContainerReadWrite: true, ObservedAt: now}, nil
	}}
	return s, d, f
}
func collectPersistent(t *testing.T, f DataFacts, d policy.Desired) target.PersistentDatabase {
	t.Helper()
	facts, err := f.Collect(context.Background(), d)
	if err != nil || facts.Status != target.KnownStatus || facts.Value == nil || len(*facts.Value) != 1 {
		t.Fatalf("facts: %+v %v", facts, err)
	}
	return (*facts.Value)[0]
}
func TestDataFactsUntouchedAllocationAndMissingHistory(t *testing.T) {
	s, d, f := persistentHostFixture(t)
	ctx := context.Background()
	first := collectPersistent(t, f, d)
	if first.Schema.State != data.AllocatedEmpty || len(first.Definitions) != 1 {
		t.Fatalf("initial proof: %+v", first)
	}
	second := collectPersistent(t, f, d)
	if second.Database != first.Database {
		t.Fatal("reservation changed")
	}
	if err := s.RecordWriterAttempt(ctx, first.Database.IncarnationID, "attempt-1"); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(string(first.Database.Root), first.Database.RelativeDirectory)
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	missing := collectPersistent(t, f, d)
	if missing.Schema.State != data.Unknown {
		t.Fatal("historical missing DB treated empty")
	}
	if _, err := os.Lstat(path); !os.IsNotExist(err) {
		t.Fatal("old missing directory recreated", err)
	}
}
func TestDataFactsNeverRepairsExistingDataAndReportsFences(t *testing.T) {
	s, d, f := persistentHostFixture(t)
	ctx := context.Background()
	first := collectPersistent(t, f, d)
	path := filepath.Join(string(first.Database.Root), first.Database.RelativeDirectory, "app.db")
	if err := os.WriteFile(path, []byte("not-sqlite"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.HoldDataFence(ctx, first.Database.DatabaseID, "fixture-fence"); err != nil {
		t.Fatal(err)
	}
	fact := collectPersistent(t, f, d)
	if fact.Schema.State != data.Unknown || !fact.Fenced {
		t.Fatalf("unsafe observed facts %+v", fact)
	}
	raw, err := os.ReadFile(path) //nolint:gosec // Path is a private test fixture or verified private probe file.
	if err != nil || string(raw) != "not-sqlite" {
		t.Fatal("data repaired", err)
	}
}
func TestDataFactsAdmissionBeforeAllocation(t *testing.T) {
	for _, mode := range []string{"denied root", "excluded root", "failed mapping"} {
		t.Run(mode, func(t *testing.T) {
			s, d, f := persistentHostFixture(t)
			switch mode {
			case "denied root":
				d.PersistentRoots = nil
			case "excluded root":
				f.ExcludedRoots = []string{string(d.Databases[0].PersistentRoot)}
			case "failed mapping":
				f.ProbeMapping = func(context.Context, localexec.Runner, data.RootEvidence, data.RuntimeIdentity) (data.MappingEvidence, error) {
					return data.MappingEvidence{}, errors.New("unmapped")
				}
			}
			facts, err := f.Collect(context.Background(), d)
			if err != nil || facts.Status != target.Unknown {
				t.Fatalf("admission %+v %v", facts, err)
			}
			if _, err = s.ActiveDataIncarnation(context.Background(), "hello"); !errors.Is(err, store.ErrNotFound) {
				t.Fatal("identity allocated before admission", err)
			}
			entries, err := os.ReadDir(string(d.Databases[0].PersistentRoot))
			if err != nil || len(entries) != 0 {
				t.Fatal("admission wrote app data", err)
			}
		})
	}
}
func TestDataCompatibilityReobservesAndRejectsOldWriter(t *testing.T) {
	s, d, f := persistentHostFixture(t)
	ctx := context.Background()
	fact := collectPersistent(t, f, d)
	c := DataCompatibility{Store: s}
	if safe, err := c.Safe(ctx, policy.Desired{}, d); err != nil || !safe {
		t.Fatal("first untouched candidate blocked", err)
	}
	previous := d
	previous.SchemaCompatibility = []data.SchemaCompatibility{{Database: "main", Startup: "preserve", Accepts: []string{"v1"}}}
	if safe, _ := c.Safe(ctx, previous, d); safe {
		t.Fatal("incompatible previous writer accepted")
	}
	if err := s.RecordWriterAttempt(ctx, fact.Database.IncarnationID, "attempt"); err != nil {
		t.Fatal(err)
	}
	if safe, _ := c.Safe(ctx, policy.Desired{}, d); safe {
		t.Fatal("consumed allocation reused")
	}
}
func TestDataWriterStartsBindsFreshCandidateThenRefusesDrift(t *testing.T) {
	s, d, f := persistentHostFixture(t)
	ctx := context.Background()
	fact := collectPersistent(t, f, d)
	raw, err := os.ReadFile("../target/testdata/ready-arm64.json") //nolint:gosec // Path is a private test fixture or verified private probe file.
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := target.Decode(raw)
	if err != nil {
		t.Fatal(err)
	}
	p, err := plan.Build(plan.Input{Desired: d, Snapshot: snapshot, Image: plan.Image{Digest: "sha256:" + strings.Repeat("a", 64), Platform: target.Platform{OS: "linux", Arch: "arm64"}, ManifestDigest: target.Known("sha256:" + strings.Repeat("c", 64))}, State: plan.BrineState{Target: snapshot.Identity, Releases: []plan.CurrentRelease{}}})
	if err != nil {
		t.Fatal(err)
	}
	// Synthetic saved start candidate isolates adapter/store state. This is not
	// evidence of a deploy or generated-unit startup.
	p.Kind = plan.Create
	p.DataMounts = []data.Mount{{Database: fact.Database, HostPath: filepath.Join(string(fact.Database.Root), fact.Database.RelativeDirectory), ContainerPath: fact.Database.MountPath, BindingID: fact.Database.ReplicaBindingID}}
	if _, err = s.SavePlan(ctx, p, d); err != nil {
		t.Fatal(err)
	}
	op, _, err := s.CreateOperation(ctx, ops.Intent{Kind: ops.Deploy, PlanID: p.Hash}, "fixture", "writer-adapter")
	if err != nil {
		t.Fatal(err)
	}
	for _, state := range []ops.State{ops.Preflight, ops.Preparing, ops.Starting} {
		if err = s.SetOperationState(ctx, op.ID, state); err != nil {
			t.Fatal(err)
		}
	}
	w := DataWriterStarts{Store: s}
	if err = w.BindWriterStart(ctx, op.ID, p, d); err != nil {
		t.Fatal(err)
	}
	if err = w.ClearWriterStart(ctx, op.ID); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(string(fact.Database.Root), fact.Database.RelativeDirectory, "app.db"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	if err = w.BindWriterStart(ctx, op.ID, p, d); err == nil {
		t.Fatal("zero-byte DB allowed startup")
	}
}

func TestDataFactsRetainSchemaRegistryAfterCandidateRemap(t *testing.T) {
	_, desired, facts := persistentHostFixture(t)
	original := data.SchemaDefinition{Database: "main", Marker: "v1", CatalogSHA256: strings.Repeat("c", 64)}
	desired.SchemaDefinitions = []data.SchemaDefinition{original}
	collectPersistent(t, facts, desired)
	desired.SchemaDefinitions[0].CatalogSHA256 = strings.Repeat("d", 64)
	changed := collectPersistent(t, facts, desired)
	found := false
	for _, definition := range changed.Definitions {
		if definition.Marker == original.Marker {
			found = true
			if definition != original {
				t.Fatal("candidate remapped immutable schema")
			}
		}
	}
	if !found || changed.Schema.State != data.AllocatedEmpty {
		t.Fatal("retained evidence lost after rejected remap")
	}
}
