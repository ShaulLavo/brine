package apply

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
	"github.com/ShaulLavo/brine/internal/spec"
	"github.com/ShaulLavo/brine/internal/target"
)

type managedPreparationFake struct {
	r                  *rig
	calls, inspections int
	prepared           bool
	failure            error
}

func (f *managedPreparationFake) PrepareManaged(ctx context.Context, _ string, _ plan.Plan, _ policy.Desired) error {
	f.calls++
	if f.r.intent != "prepare_managed_engine" {
		panic("native allocation preceded journal intent")
	}
	if _, ok := ctx.Deadline(); !ok {
		panic("unbounded native allocation")
	}
	return f.failure
}
func (f *managedPreparationFake) ManagedPrepared(ctx context.Context, _ string, _ plan.Plan, _ policy.Desired) (bool, error) {
	f.inspections++
	if _, ok := ctx.Deadline(); !ok {
		panic("unbounded native inspection")
	}
	return f.prepared, nil
}

func managedPreparationRig(t *testing.T, frontend bool) *rig {
	t.Helper()
	r := newRig(t, false)
	raw, err := os.ReadFile("../spec/testdata/managed.toml")
	if err != nil {
		t.Fatal(err)
	}
	if frontend {
		raw = []byte("image=\"registry.example.com:5000/team/web@sha256:" + strings.Repeat("a", 64) + "\"\ncontainer_port=8080\nresources={memory_mb=128,pids_limit=128}\n" + string(raw))
	}
	app, err := spec.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	raw, err = os.ReadFile("../policy/testdata/managed-operator.toml")
	if err != nil {
		t.Fatal(err)
	}
	pol, err := policy.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	desired, err := policy.Normalize(app, pol)
	if err != nil {
		t.Fatal(err)
	}
	desired.SpacetimeDB.Image = spec.ImageReference(data.SpacetimeDBImage)
	_, digest, _ := strings.Cut(data.SpacetimeDBImage, "@")
	image := plan.Image{Digest: digest, Platform: target.Platform{OS: "linux", Arch: "arm64"}, ManifestDigest: target.Known(data.SpacetimeDBARM64Manifest)}
	in := r.facts.Input
	in.Desired = desired
	in.Image = image
	in.Managed = &plan.ManagedFacts{EngineImage: image, Fences: []data.QuiescenceFence{}}
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
	proposed := plan.ManagedServicePlan{State: data.EngineUnpublished, EngineImage: image, EnginePort: 20000, Database: data.SpacetimeDBBinding{DatabaseID: database, IncarnationID: incarnation, Name: desired.SpacetimeDB.Name, Root: desired.SpacetimeDB.PersistentRoot, RelativeDirectory: relative, BindingID: binding}, Archive: data.ArchiveBinding{BindingID: binding, DatabaseID: database, Destination: destination, EpochID: epoch, RemotePrefix: prefix}}
	if frontend {
		in.Image = plan.Image{Digest: "sha256:" + strings.Repeat("a", 64), Platform: image.Platform, ManifestDigest: target.Known("sha256:" + strings.Repeat("c", 64))}
		proposed.Frontend = &plan.ManagedFrontendPlan{Image: in.Image, Port: 20001, Secrets: []plan.SecretBinding{}}
	}
	root := data.RootEvidence{Root: proposed.Database.Root, Device: 7, Inode: 9, Filesystem: "ext4", FreeBytes: desired.MinimumFreeDiskBytes + 10000, FreeInodes: 100, ObservedAt: time.Now().UTC()}
	planned, err := plan.BuildManagedPreparation(in, proposed, root)
	if err != nil || planned.Kind != plan.Create {
		t.Fatalf("managed preparation %v %v", planned.Conflicts, err)
	}
	r.plan = planned
	r.desired = desired
	r.facts.Input = in
	return r
}

func TestManagedDeployNoInitPreparationUsesOnlyOneJournaledAllocation(t *testing.T) {
	for _, frontend := range []bool{false, true} {
		r := managedPreparationRig(t, frontend)
		fake := &managedPreparationFake{r: r, prepared: true}
		r.executor.ManagedPreparation = fake
		if err := r.executor.Run(context.Background(), "op", r.plan, r.desired); err != nil {
			t.Fatal(err)
		}
		if r.state != Succeeded || fake.calls != 1 || fake.inspections != 1 || len(r.effects) != 0 || r.committed {
			t.Fatal("preparation started/published/committed an app", r.effects)
		}
	}
}

func TestManagedReconcileUnknownPreparationDoesNotRetry(t *testing.T) {
	r := managedPreparationRig(t, false)
	fake := &managedPreparationFake{r: r, failure: errors.New("partial filesystem effect")}
	r.executor.ManagedPreparation = fake
	if err := r.executor.Run(context.Background(), "op", r.plan, r.desired); err == nil {
		t.Fatal("partial native effect reported success")
	}
	if r.state != RecoveryRequired || fake.calls != 1 || fake.inspections != 2 || len(r.effects) != 0 || r.committed {
		t.Fatal("unknown preparation was retried or served", r.state, fake.calls, fake.inspections, r.effects)
	}
}
