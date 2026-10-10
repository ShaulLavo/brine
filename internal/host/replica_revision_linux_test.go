//go:build linux

package host

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"syscall"
	"testing"
	"time"

	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/plan"
	"github.com/ShaulLavo/brine/internal/replication"
)

type interruptedPublisher struct {
	replication.ArtifactPublisher
	once bool
}

func (p *interruptedPublisher) PublishConfig(ctx context.Context, a replication.Artifacts) error {
	if err := p.ArtifactPublisher.PublishConfig(ctx, a); err != nil {
		return err
	}
	if p.once {
		p.once = false
		return replication.ErrPublish
	}
	return nil
}

func activePreparationFixture(t *testing.T) preparationFixture {
	t.Helper()
	f := newPreparationFixture(t)
	if err := f.preparation.PreparePersistent(context.Background(), "initial", f.planned, f.desired); err != nil {
		t.Fatal(err)
	}
	f.planned.Lifecycle = plan.ReviseReplica
	return f
}

func TestReplicaCadenceFenceStoresOnlyAndResumes(t *testing.T) {
	ctx := context.Background()
	f := activePreparationFixture(t)
	before, err := f.preparation.State.ReadReplicaPermit(ctx, f.fact.Database.DatabaseID)
	if err != nil {
		t.Fatal(err)
	}
	fence, err := f.preparation.State.HoldDataFence(ctx, f.fact.Database.DatabaseID, "fence-owner")
	if err != nil {
		t.Fatal(err)
	}
	f.desired.Databases[0].SyncInterval = 2 * time.Minute
	artifacts, after, err := f.preparation.expected(ctx, f.planned, f.desired)
	if err != nil {
		t.Fatal(err)
	}
	if err = f.preparation.PreparePersistent(ctx, "fenced-cadence", f.planned, f.desired); !errors.Is(err, replication.ErrRestartUnknown) {
		t.Fatal("fenced revision not deferred", err)
	}
	stored, err := f.preparation.State.ReadReplicaRevision(ctx, revisionID("fenced-cadence", after[0]))
	if err != nil || stored.Stage != data.RevisionStored || stored.After != after[0] {
		t.Fatal("pending revision not stored", err)
	}
	if f.services.stops != 0 || f.services.starts != 1 {
		t.Fatal("fenced storage touched replica")
	}
	if _, err = os.Lstat(artifacts[0].ConfigPath); !os.IsNotExist(err) {
		t.Fatal("fenced revision published files", err)
	}
	held, err := f.preparation.State.ReadReplicaPermit(ctx, f.fact.Database.DatabaseID)
	if err != nil || held.Replica != before.Replica {
		t.Fatal("fenced revision changed active binding", err)
	}
	if _, err = f.preparation.State.CandidateWriterSchema(ctx, f.fact.Database.IncarnationID, f.desired); err == nil {
		t.Fatal("revision storage admitted an app writer through a fence")
	}
	if ready, err := f.preparation.InspectReplicaRevision(ctx, "fenced-cadence", f.planned, f.desired); err != nil || ready {
		t.Fatal("held fence authorized resume", err)
	}
	if err = f.preparation.State.ReleaseDataFence(ctx, fence.ID, "fence-owner"); err != nil {
		t.Fatal(err)
	}
	if ready, err := f.preparation.InspectReplicaRevision(ctx, "fenced-cadence", f.planned, f.desired); err != nil || !ready {
		t.Fatal("released pending revision not resumable", err)
	}
	if err = f.preparation.PreparePersistent(ctx, "fenced-cadence", f.planned, f.desired); err != nil {
		t.Fatal(err)
	}
	activated, err := f.preparation.State.ReadReplicaPermit(ctx, f.fact.Database.DatabaseID)
	if err != nil || activated.Replica != after[0] || f.services.stops != 1 || f.services.starts != 2 {
		t.Fatal("resume did not activate exactly once", err)
	}
}

func TestReplicaCadenceUnchangedHasNoEffects(t *testing.T) {
	ctx := context.Background()
	f := activePreparationFixture(t)
	before, err := f.preparation.State.ReadReplicaPermit(ctx, f.fact.Database.DatabaseID)
	if err != nil {
		t.Fatal(err)
	}
	lockBefore, err := os.Stat(before.Replica.LifetimeLockFile)
	if err != nil {
		t.Fatal(err)
	}
	f.manager.DaemonReloadFunc = func(context.Context) error { t.Fatal("unchanged cadence reloaded units"); return nil }
	for _, operation := range []string{"initial", "unchanged"} {
		if err = f.preparation.PreparePersistent(ctx, operation, f.planned, f.desired); err != nil {
			t.Fatal(err)
		}
	}
	after, err := f.preparation.State.ReadReplicaPermit(ctx, f.fact.Database.DatabaseID)
	if err != nil || !reflect.DeepEqual(before, after) || f.services.stops != 0 || f.services.starts != 1 {
		t.Fatal("unchanged cadence had effects", err)
	}
	lockAfter, err := os.Stat(after.Replica.LifetimeLockFile)
	if err != nil || lockBefore.Sys().(*syscall.Stat_t).Ino != lockAfter.Sys().(*syscall.Stat_t).Ino {
		t.Fatal("stable lifetime lock replaced", err)
	}
}

func TestReplicaCadenceCycleReusesImmutableBytes(t *testing.T) {
	ctx := context.Background()
	f := activePreparationFixture(t)
	original, err := f.preparation.State.ReadReplicaPermit(ctx, f.fact.Database.DatabaseID)
	if err != nil {
		t.Fatal(err)
	}
	originalBytes, err := os.ReadFile(original.Replica.ConfigFile)
	if err != nil {
		t.Fatal(err)
	}
	for i, interval := range []time.Duration{2 * time.Minute, time.Minute} {
		f.desired.Databases[0].SyncInterval = interval
		if err = f.preparation.PreparePersistent(ctx, []string{"cycle-two", "cycle-one"}[i], f.planned, f.desired); err != nil {
			t.Fatal(err)
		}
	}
	current, err := f.preparation.State.ReadReplicaPermit(ctx, f.fact.Database.DatabaseID)
	if err != nil || current.Replica != original.Replica || f.services.stops != 2 || f.services.starts != 3 {
		t.Fatal("cadence cycle did not restore the exact immutable binding", err)
	}
	currentBytes, err := os.ReadFile(original.Replica.ConfigFile)
	if err != nil || string(currentBytes) != string(originalBytes) {
		t.Fatal("cadence cycle replaced original config bytes", err)
	}
}

func TestReplicaCadenceInterruptedBoundariesInspectBeforeResume(t *testing.T) {
	testReplicaInterruptions(t, false)
}
func TestReplicaMixedDeploymentInterruptedBoundariesResumeOnlyCursor(t *testing.T) {
	testReplicaInterruptions(t, true)
}

func testReplicaInterruptions(t *testing.T, mixed bool) {
	for _, boundary := range []string{"publish", "lock", "service_before_commit", "commit_before_restart", "stop_unknown", "start_unknown"} {
		t.Run(boundary, func(t *testing.T) {
			ctx := context.Background()
			f := activePreparationFixture(t)
			if mixed {
				f.planned.Lifecycle = ""
			}
			before, err := f.preparation.State.ReadReplicaPermit(ctx, f.fact.Database.DatabaseID)
			if err != nil {
				t.Fatal(err)
			}
			lockBefore, err := os.Stat(before.Replica.LifetimeLockFile)
			if err != nil {
				t.Fatal(err)
			}
			f.desired.Databases[0].SyncInterval = 2 * time.Minute
			f.desired.Backup.SnapshotInterval = 12 * time.Hour
			var release func() error
			switch boundary {
			case "publish":
				f.preparation.Publisher = &interruptedPublisher{ArtifactPublisher: f.preparation.Publisher.(replication.ArtifactPublisher), once: true}
			case "lock":
				lock, err := replication.AcquireLifetimeLock(ctx, before.Replica.LifetimeLockFile)
				if err != nil {
					t.Fatal(err)
				}
				release = lock.Release
			case "service_before_commit":
				db, err := sql.Open("sqlite", filepath.Join(f.preparation.StateRoot, "control.db"))
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() {
					if err := db.Close(); err != nil {
						t.Error(err)
					}
				})
				if _, err = db.Exec(`CREATE TRIGGER fixture_commit_failure BEFORE INSERT ON data_replica_commits BEGIN SELECT RAISE(ABORT,'fixture interruption'); END`); err != nil {
					t.Fatal(err)
				}
				release = func() error { _, err := db.Exec(`DROP TRIGGER fixture_commit_failure`); return err }
			case "commit_before_restart":
				f.manager.DaemonReloadFunc = func(context.Context) error { return replication.ErrRestartUnknown }
				release = func() error { f.manager.DaemonReloadFunc = func(context.Context) error { return nil }; return nil }
			case "stop_unknown":
				f.services.unknownStop = true
			case "start_unknown":
				f.services.unknownStart = true
			}
			if err = f.preparation.PreparePersistent(ctx, "interrupted-cadence", f.planned, f.desired); !errors.Is(err, replication.ErrRestartUnknown) {
				t.Fatal("interruption not retained", err)
			}
			stops, starts := f.services.stops, f.services.starts
			if boundary == "stop_unknown" || boundary == "start_unknown" {
				if ready, _ := f.preparation.InspectReplicaRevision(ctx, "interrupted-cadence", f.planned, f.desired); ready {
					t.Fatal("uncertain attempt prematurely resumable")
				}
				if err = f.preparation.PreparePersistent(ctx, "interrupted-cadence", f.planned, f.desired); err == nil || f.services.stops != stops || f.services.starts != starts {
					t.Fatal("unknown attempt was reissued", err)
				}
				f.services.active = boundary == "start_unknown" // Independent manager settlement.
			}
			if release != nil {
				if err = release(); err != nil {
					t.Fatal(err)
				}
			}
			if ready, err := f.preparation.InspectReplicaRevision(ctx, "interrupted-cadence", f.planned, f.desired); err != nil || !ready {
				t.Fatal("settled cursor not resumable", err)
			}
			resume := f.preparation.PreparePersistent
			if mixed {
				f.preparation.Runner, f.preparation.ProbeRoot, f.preparation.ProbeMapping = nil, nil, nil
				resume = f.preparation.ResumeReplicaRevision
			}
			if err = resume(ctx, "interrupted-cadence", f.planned, f.desired); err != nil {
				t.Fatal(err)
			}
			after, err := f.preparation.State.ReadReplicaPermit(ctx, f.fact.Database.DatabaseID)
			if err != nil || after.Replica.EpochID != before.Replica.EpochID || after.Replica.BindingID != before.Replica.BindingID || after.Replica.RemotePrefix != before.Replica.RemotePrefix || f.services.stops != 1 || f.services.starts != 2 {
				t.Fatal("resume changed identity or repeated restart", err)
			}
			cadence, err := replication.ConfigCadence([]byte(after.Replica.ConfigContent))
			if err != nil || cadence.SyncInterval != 2*time.Minute || cadence.SnapshotInterval != 12*time.Hour {
				t.Fatal("wrong revised cadence", err)
			}
			lockAfter, err := os.Stat(after.Replica.LifetimeLockFile)
			if err != nil || lockBefore.Sys().(*syscall.Stat_t).Ino != lockAfter.Sys().(*syscall.Stat_t).Ino {
				t.Fatal("restart replaced lifetime lock", err)
			}
			if ready, err := f.preparation.PersistentPrepared(ctx, "interrupted-cadence", f.planned, f.desired); err != nil || !ready {
				t.Fatal("completed revision not independently proven", err)
			}
		})
	}
}
