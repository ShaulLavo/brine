package plan

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/spec"
	"github.com/ShaulLavo/brine/internal/target"
)

func managedPreparationFixture(t *testing.T, frontend bool) (Input, ManagedServicePlan, data.RootEvidence) {
	t.Helper()
	in, _ := managedPlanInput(t, frontend)
	in.Snapshot = fixture(t, "ready-arm64").Snapshot
	in.Desired.SpacetimeDB.Image = spec.ImageReference(data.SpacetimeDBImage)
	_, digest, _ := strings.Cut(data.SpacetimeDBImage, "@")
	image := Image{Digest: digest, Platform: target.Platform{OS: "linux", Arch: "arm64"}, ManifestDigest: target.Known(data.SpacetimeDBARM64Manifest)}
	in.Managed = &ManagedFacts{EngineImage: image, Fences: []data.QuiescenceFence{}}
	incarnation, database, binding, epoch := data.AppIncarnationID(strings.Repeat("a", 32)), data.DatabaseID(strings.Repeat("b", 32)), data.ReplicaBindingID(strings.Repeat("c", 32)), data.ReplicaEpochID(strings.Repeat("d", 32))
	relative, err := data.RelativeDirectory(incarnation, database)
	if err != nil {
		t.Fatal(err)
	}
	destination := in.Desired.BackupDestinations[0]
	prefix, err := data.RemotePrefix(destination.BasePrefix, incarnation, database, epoch)
	if err != nil {
		t.Fatal(err)
	}
	proposed := ManagedServicePlan{State: data.EngineUnpublished, EngineImage: image, EnginePort: 20000, Database: data.SpacetimeDBBinding{DatabaseID: database, Name: in.Desired.SpacetimeDB.Name, IncarnationID: incarnation, Root: in.Desired.SpacetimeDB.PersistentRoot, RelativeDirectory: relative, BindingID: binding}, Archive: data.ArchiveBinding{BindingID: binding, DatabaseID: database, Destination: destination, EpochID: epoch, RemotePrefix: prefix}}
	if frontend {
		in.Image.ManifestDigest = target.Known(in.Image.Digest)
		proposed.Frontend = &ManagedFrontendPlan{Image: in.Image, Port: 20001, Secrets: []SecretBinding{}}
	}
	root := data.RootEvidence{Root: proposed.Database.Root, Device: 7, Inode: 9, Filesystem: "ext4", FreeBytes: in.Desired.MinimumFreeDiskBytes + 10000, FreeInodes: 100, ObservedAt: time.Now().UTC()}
	return in, proposed, root
}

func TestManagedPreparationHasOnlyUnpublishedAllocationEffects(t *testing.T) {
	for _, frontend := range []bool{false, true} {
		testManagedPreparation(t, frontend)
	}
}

func testManagedPreparation(t *testing.T, frontend bool) {
	t.Helper()
	in, proposal, root := managedPreparationFixture(t, frontend)
	planned, err := BuildManagedPreparation(in, proposal, root)
	if err != nil {
		t.Fatal(err)
	}
	if planned.Kind != Create || len(planned.Conflicts) != 0 || len(planned.Changes) != 1 || planned.Changes[0].Kind != PrepareManagedEngine || planned.Managed == nil || planned.Managed.State != data.EngineUnpublished || planned.Managed.RuntimeSHA256 != "" || planned.Managed.EngineUnitHash != "" || len(planned.DataAllocations) != 0 || len(planned.DataSchemas) != 0 || len(planned.DataMounts) != 0 || planned.Runtime != nil || planned.Backup != nil {
		t.Fatalf("unexpected preparation intent: kind=%s conflicts=%v", planned.Kind, planned.Conflicts)
	}
	first, err := planned.CanonicalBytes()
	if err != nil {
		t.Fatal(err)
	}
	root.FreeBytes += 5000
	root.FreeInodes += 300
	root.ObservedAt = root.ObservedAt.Add(time.Hour)
	same, err := BuildManagedPreparation(in, proposal, root)
	if err != nil {
		t.Fatal(err)
	}
	second, _ := same.CanonicalBytes()
	if !bytes.Equal(first, second) {
		t.Fatal("irrelevant measurement time/capacity changed preparation")
	}
	in.Snapshot.UsedPorts = target.Known([]target.Port{proposal.EnginePort})
	refused, err := BuildManagedPreparation(in, proposal, root)
	if err != nil {
		t.Fatal(err)
	}
	if refused.Kind != Conflict || len(refused.Changes) != 0 {
		t.Fatal("foreign port use admitted")
	}
}

func TestManagedPreparationRequiresCurrentFactsAndDistinctReservations(t *testing.T) {
	in, proposal, root := managedPreparationFixture(t, true)
	in.State.ManagedReservations = []target.ManagedPortReservation{{App: "other", BindingID: data.ReplicaBindingID(strings.Repeat("e", 32)), EnginePort: proposal.EnginePort, FrontendPort: proposal.Frontend.Port}}
	planned, err := BuildManagedPreparation(in, proposal, root)
	if err != nil {
		t.Fatal(err)
	}
	if planned.Kind != Conflict || len(planned.Changes) != 0 {
		t.Fatal("prepared ports allocated again")
	}
	in.State.ManagedReservations = nil
	changed := proposal.Clone()
	changed.Frontend.Image.ManifestDigest = target.Known("sha256:" + strings.Repeat("f", 64))
	if _, err := BuildManagedPreparation(in, changed, root); err == nil {
		t.Fatal("changed frontend manifest admitted")
	}
	root.Device = 0
	planned, err = BuildManagedPreparation(in, proposal, root)
	if err != nil {
		t.Fatal(err)
	}
	if planned.Kind != Conflict || len(planned.Changes) != 0 {
		t.Fatal("unmeasured root admitted")
	}
}
