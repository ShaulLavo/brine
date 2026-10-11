//go:build linux

package store

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/ShaulLavo/brine/internal/archive"
	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/plan"
	"github.com/ShaulLavo/brine/internal/policy"
	"github.com/ShaulLavo/brine/internal/spec"
	"github.com/ShaulLavo/brine/internal/target"
)

func TestArchiveOnlyRetirementThroughProductionCollector(t *testing.T) {
	for _, published := range []bool{false, true} {
		t.Run(map[bool]string{false: "prepared only", true: "published"}[published], func(t *testing.T) {
			ctx := context.Background()
			s := openTest(t)
			collector, runner, _, _, _ := inventoryLifecycleFixture(t, s)
			runner["podman --remote=false secret ls --format {{.ID}} {{.Name}}"] = ""
			initial, err := collector.Collect(ctx)
			if err != nil {
				t.Fatal(err)
			}
			p := storedArchivePlan(t, s)
			in := fixture(t)
			in.Snapshot = initial
			in.State.Target = initial.Identity
			in.Desired.Name = spec.Name(p.Candidate.App)
			in.Desired.Secrets = []policy.Secret{}
			if published {
				created, err := plan.Build(in)
				if err != nil || created.Kind != plan.Create {
					t.Fatal(created.Conflicts, err)
				}
				id, err := s.SavePlan(ctx, created, in.Desired)
				if err != nil {
					t.Fatal(err)
				}
				r := Release{ID: "archived-release", PlanID: id, Image: created.Image, HostPort: created.HostPort, Secrets: created.Secrets, Units: []target.Unit{{Name: p.Candidate.App + ".container", Hash: "sha256:" + strings.Repeat("b", 64)}}, CaddyFile: target.CaddyFile{Name: p.Candidate.App + ".caddy", Hash: "sha256:" + strings.Repeat("c", 64)}}
				if err = s.CommitRelease(ctx, p.Candidate.App, r); err != nil {
					t.Fatal(err)
				}
				p.Candidate.ReleaseID, p.Generation = r.ID, 1
				archiveID, err := data.NewID()
				if err != nil {
					t.Fatal(err)
				}
				p.ArchiveID = data.ArchiveID(archiveID)
				p.Trees[0].ArchiveRelative = "archives/" + archiveID
				p.ID, err = p.Fingerprint()
				if err != nil {
					t.Fatal(err)
				}
				if err = s.SaveArchivePlan(ctx, p); err != nil {
					t.Fatal(err)
				}
			}
			p, op, _ := preparedStoredArchive(t, s, p)
			state := archive.Disposition{}
			for _, stage := range []archive.DispositionStage{archive.CleanupIntent, archive.RuntimeRetired, archive.RenameIntent, archive.Renamed} {
				next := archive.Disposition{ArchiveID: p.ArchiveID, Sequence: state.Sequence + 1, Stage: stage}
				state, err = s.AdvanceArchiveDisposition(ctx, op, state, next)
				if err != nil {
					t.Fatal(err)
				}
			}
			done := archive.Disposition{ArchiveID: p.ArchiveID, Sequence: state.Sequence + 1, Stage: archive.Retired, CommittedAt: time.Now().UTC()}
			done.RetainUntil = done.CommittedAt.Add(30 * 24 * time.Hour)
			if _, err = s.FinishArchiveDisposition(ctx, op, state, done); err != nil {
				t.Fatal(err)
			}
			control, err := s.InventoryState(ctx)
			if err != nil || published && (control.Target == nil || *control.Target != initial.Identity) {
				t.Fatal("archive retirement lost deployment target", control, err)
			}
			var removals int
			if err = s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM app_removals").Scan(&removals); err != nil || removals != 0 {
				t.Fatal("archive-only fixture has removal receipts", removals, err)
			}
			for _, recreate := range []bool{false, true} {
				if recreate {
					if _, err = s.ReserveDatabase(ctx, dataRequest()); err != nil {
						t.Fatal(err)
					}
				}
				observed, err := collector.Collect(ctx)
				if err != nil {
					t.Fatal(err)
				}
				for _, app := range *observed.Apps.Value {
					if app.Name == p.Candidate.App && app.AllocatedHostPort.Status != target.Absent {
						t.Fatalf("retired app still reserves a port (recreated=%t): %+v", recreate, app)
					}
				}
				in.Snapshot = observed
				in.State, err = s.LoadBrineState(ctx, observed.Identity, *observed.Generation.Value)
				if err != nil {
					t.Fatal(err)
				}
				in.Desired.Name = "another"
				created, err := plan.Build(in)
				if err != nil || created.Kind != plan.Create || created.HostPort != 20000 {
					t.Fatalf("archive-only history blocked another app (recreated=%t): %+v %v", recreate, created, err)
				}
			}
			if published {
				runner[`podman --remote=false ps --all --format {{.Names}} {{.Label "PODMAN_SYSTEMD_UNIT"}}`] = "systemd-" + p.Candidate.App + " " + p.Candidate.App + ".service"
				in.Snapshot, err = collector.Collect(ctx)
				if err != nil {
					t.Fatal(err)
				}
				in.Desired.Name = spec.Name(p.Candidate.App)
				own, err := plan.Build(in)
				if err != nil || own.Kind != plan.Conflict {
					t.Fatal("unknown own runtime accepted", own.Conflicts, err)
				}
				unknownPort := false
				for _, conflict := range own.Conflicts {
					if conflict.Code == "unknown_facts" && conflict.Field == "app.port" {
						unknownPort = true
					}
				}
				if !unknownPort {
					t.Fatal("own runtime port was not unknown", own.Conflicts)
				}
				collector.IdentityKey = []byte("another-fixture-target")
				foreign, err := collector.Collect(ctx)
				if err != nil || foreign.Generation.Status != target.Unknown {
					t.Fatal("archive retirement trusted on another target", foreign.Generation, err)
				}
			}
		})
	}
}

func TestPreparedOnlyAppAlongsideCommittedRelease(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	storedArchivePlan(t, s)
	collector, _, _, _, _ := inventoryLifecycleFixture(t, s)
	in := fixture(t)
	var err error
	in.Snapshot, err = collector.Collect(ctx)
	if err != nil {
		t.Fatal(err)
	}
	in.State.Target = in.Snapshot.Identity
	created, err := plan.Build(in)
	if err != nil || created.Kind != plan.Create {
		t.Fatal(created.Conflicts, err)
	}
	id, err := s.SavePlan(ctx, created, in.Desired)
	if err != nil {
		t.Fatal(err)
	}
	r := Release{ID: "deployed-release", PlanID: id, Image: created.Image, HostPort: created.HostPort, Secrets: created.Secrets, Units: []target.Unit{{Name: "hello.container", Hash: "sha256:" + strings.Repeat("b", 64)}}, CaddyFile: target.CaddyFile{Name: "hello.caddy", Hash: "sha256:" + strings.Repeat("c", 64)}}
	if err = s.CommitRelease(ctx, "hello", r); err != nil {
		t.Fatal(err)
	}
	in.Snapshot, err = collector.Collect(ctx)
	if err != nil {
		t.Fatal(err)
	}
	in.State, err = s.LoadBrineState(ctx, in.Snapshot.Identity, *in.Snapshot.Generation.Value)
	if err != nil {
		t.Fatal(err)
	}
	in.Snapshot.CaddyConfig = target.Known(target.CaddyConfigSet{Generation: r.CaddyGeneration, Files: []target.CaddyFile{r.CaddyFile}})
	in.Snapshot.LiveCaddyFiles = target.Known([]target.LiveCaddyFile{{Name: r.CaddyFile.Name, App: "hello", Domains: target.Known([]string{"hello.example.com"})}})
	in.Desired.Name, in.Desired.Secrets = "another", []policy.Secret{}
	in.Desired.Domains = []spec.Domain{"another.example.test"}
	created, err = plan.Build(in)
	if err != nil || created.Kind != plan.Create || created.HostPort == r.HostPort {
		t.Fatal("prepared app blocked admission or committed port reused", created, err)
	}
}
