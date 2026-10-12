package inventory

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/plan"
	"github.com/ShaulLavo/brine/internal/policy"
	"github.com/ShaulLavo/brine/internal/spec"
	"github.com/ShaulLavo/brine/internal/target"
)

func collectCaddyVersion(t *testing.T, output string) target.Observation[string] {
	t.Helper()
	collector := Collector{FS: baseFixture(), Runner: fakeRunner{"uname -m": "aarch64", "caddy version": output}, IdentityKey: []byte("version-regression")}
	snapshot, err := collector.Collect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return snapshot.Versions.Caddy
}

func collectedCaddyPreparationFixture(t *testing.T) (plan.Input, plan.ManagedServicePlan, data.RootEvidence) {
	t.Helper()
	rawSnapshot, err := os.ReadFile("../target/testdata/ready-arm64.json")
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := target.Decode(rawSnapshot)
	if err != nil {
		t.Fatal(err)
	}
	rawApp, err := os.ReadFile("../spec/testdata/managed.toml")
	if err != nil {
		t.Fatal(err)
	}
	app, err := spec.Parse(rawApp)
	if err != nil {
		t.Fatal(err)
	}
	rawOperator, err := os.ReadFile("../policy/testdata/managed-operator.toml")
	if err != nil {
		t.Fatal(err)
	}
	operator, err := policy.Parse(rawOperator)
	if err != nil {
		t.Fatal(err)
	}
	desired, err := policy.Normalize(app, operator)
	if err != nil {
		t.Fatal(err)
	}
	desired.SpacetimeDB.Image = spec.ImageReference(data.SpacetimeDBImage)
	_, digest, _ := strings.Cut(data.SpacetimeDBImage, "@")
	image := plan.Image{Digest: digest, Platform: target.Platform{OS: "linux", Arch: "arm64"}, ManifestDigest: target.Known(data.SpacetimeDBARM64Manifest)}
	in := plan.Input{Desired: desired, Snapshot: snapshot, State: plan.BrineState{Target: snapshot.Identity, Generation: *snapshot.Generation.Value, Releases: []plan.CurrentRelease{}}, Managed: &plan.ManagedFacts{EngineImage: image, Fences: []data.QuiescenceFence{}}}
	incarnation, database, binding, epoch := data.AppIncarnationID(strings.Repeat("a", 32)), data.DatabaseID(strings.Repeat("b", 32)), data.ReplicaBindingID(strings.Repeat("c", 32)), data.ReplicaEpochID(strings.Repeat("d", 32))
	relative, err := data.RelativeDirectory(incarnation, database)
	if err != nil {
		t.Fatal(err)
	}
	destination := desired.BackupDestinations[0]
	prefix, err := data.RemotePrefix(destination.BasePrefix, incarnation, database, epoch)
	if err != nil {
		t.Fatal(err)
	}
	proposed := plan.ManagedServicePlan{State: data.EngineUnpublished, EngineImage: image, EnginePort: 20000, Database: data.SpacetimeDBBinding{DatabaseID: database, Name: desired.SpacetimeDB.Name, IncarnationID: incarnation, Root: desired.SpacetimeDB.PersistentRoot, RelativeDirectory: relative, BindingID: binding}, Archive: data.ArchiveBinding{BindingID: binding, DatabaseID: database, Destination: destination, EpochID: epoch, RemotePrefix: prefix}}
	root := data.RootEvidence{Root: proposed.Database.Root, Device: 7, Inode: 9, Filesystem: "ext4", FreeBytes: desired.MinimumFreeDiskBytes + 10000, FreeInodes: 100, ObservedAt: time.Now().UTC()}
	return in, proposed, root
}

func TestManagedPreparationCollectedCaddyVersion(t *testing.T) {
	for _, tc := range []struct {
		name, output, token string
		conflict            plan.ConflictCode
	}{
		{"official", "v2.10.2 h1:upstream-build-checksum\n", "v2.10.2", ""},
		{"bare", "2.10.2\n", "2.10.2", ""},
		{"named", "caddy v2.10.2 h1:upstream-build-checksum\n", "v2.10.2", ""},
		{"old", "v2.6.2 h1:upstream-build-checksum\n", "v2.6.2", plan.RuntimeUnavailable},
		{"different patch", "v2.10.3 h1:upstream-build-checksum\n", "v2.10.3", plan.RuntimeUnavailable},
		{"prefix collision", "v2.10.20\n", "v2.10.20", plan.RuntimeUnavailable},
		{"malformed", "vv2.10.2\n", "", plan.UnknownFacts},
		{"empty", "", "", plan.UnknownFacts},
	} {
		t.Run(tc.name, func(t *testing.T) { testCollectedCaddyPlan(t, tc.output, tc.token, tc.conflict) })
	}
}

func testCollectedCaddyPlan(t *testing.T, output, token string, conflict plan.ConflictCode) {
	t.Helper()
	in, proposed, root := collectedCaddyPreparationFixture(t)
	in.Snapshot.Versions.Caddy = collectCaddyVersion(t, output)
	observed := in.Snapshot.Versions.Caddy
	if token != "" && (observed.Status != target.KnownStatus || observed.Value == nil || *observed.Value != token) {
		t.Fatalf("collected version differs: %#v", observed)
	}
	planned, err := plan.BuildManagedPreparation(in, proposed, root)
	if err != nil {
		t.Fatal(err)
	}
	if conflict == "" {
		if planned.Kind != plan.Create || len(planned.Conflicts) != 0 || len(planned.Changes) != 1 || planned.Changes[0].Kind != plan.PrepareManagedEngine {
			t.Fatalf("collected version refused: kind=%s conflicts=%v", planned.Kind, planned.Conflicts)
		}
		return
	}
	if planned.Kind != plan.Conflict || len(planned.Changes) != 0 {
		t.Fatalf("unqualified version admitted: kind=%s changes=%v", planned.Kind, planned.Changes)
	}
	for _, found := range planned.Conflicts {
		if found.Code == conflict && found.Field == "versions.caddy" {
			return
		}
	}
	t.Fatalf("Caddy refusal missing: %v", planned.Conflicts)
}
