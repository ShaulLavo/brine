//go:build linux

package store

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/ShaulLavo/brine/internal/plan"
	"github.com/ShaulLavo/brine/internal/spec"
	"github.com/ShaulLavo/brine/internal/target"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/ShaulLavo/brine/internal/archive"
	"github.com/ShaulLavo/brine/internal/data"
)

func preparedStoredArchive(t *testing.T, s *Store, supplied ...archive.Plan) (archive.Plan, archive.Operation, archive.Manifest) {
	t.Helper()
	ctx := context.Background()
	var p archive.Plan
	if len(supplied) == 0 {
		p = storedArchivePlan(t, s)
	} else {
		p = supplied[0]
	}
	o, err := s.ClaimArchive(ctx, p)
	if err != nil {
		t.Fatal(err)
	}
	for _, stage := range []archive.Stage{archive.WritersStopIntent, archive.WritersStopped, archive.CoverageIntent} {
		o, err = s.AdvanceArchive(ctx, o, stage)
		if err != nil {
			t.Fatal(err)
		}
	}
	d := sqliteArchiveDatabase(t, p.Candidate.Databases[0])
	fresh := *d.Allocation
	fresh.AllocatedAt = time.Now().UTC()
	coverage := []archive.Coverage{archive.SQLiteCoverageVariant(archive.SQLiteCoverage{DatabaseID: d.Binding.DatabaseID, WritersQuiescedAt: fresh.AllocatedAt, Empty: &fresh, Observation: data.SQLiteSchemaObservation{DatabaseID: d.Binding.DatabaseID, State: data.AllocatedEmpty, Marker: data.EmptyMarker, CatalogSHA256: data.EmptyCatalogSHA256, ObservedAt: fresh.AllocatedAt}})}
	o, err = s.SaveArchiveCoverage(ctx, o, coverage)
	if err != nil {
		t.Fatal(err)
	}
	for _, stage := range []archive.Stage{archive.ReplicaStopIntent, archive.Quiesced} {
		o, err = s.AdvanceArchive(ctx, o, stage)
		if err != nil {
			t.Fatal(err)
		}
	}
	m := archive.Manifest{Plan: p, OperationID: o.ID, Coverage: coverage, QuiescedAt: time.Now().UTC(), PreparedAt: time.Now().UTC()}
	o, err = s.SaveArchiveManifest(ctx, o, m)
	if err != nil {
		t.Fatal(err)
	}
	return p, o, m
}
func TestArchiveAbandonAtomicallyReleasesOnlyOwnFencesAndAllowsNewAttempt(t *testing.T) {
	s := openTest(t)
	p, o, m := preparedStoredArchive(t, s)
	ctx := context.Background()
	empty := archive.Disposition{}
	intent := archive.Disposition{ArchiveID: p.ArchiveID, Sequence: 1, Stage: archive.AbandonIntent}
	state, err := s.AdvanceArchiveDisposition(ctx, o, empty, intent)
	if err != nil {
		t.Fatal(err)
	}
	wrong := archive.Disposition{ArchiveID: p.ArchiveID, Sequence: 2, Stage: archive.Retired, CommittedAt: time.Now().UTC()}
	if _, err = s.FinishArchiveDisposition(ctx, o, state, wrong); !errors.Is(err, ErrConflict) {
		t.Fatal("crossed disposition branches", err)
	}
	done := archive.Disposition{ArchiveID: p.ArchiveID, Sequence: 2, Stage: archive.Abandoned, CommittedAt: time.Now().UTC()}
	if _, err = s.FinishArchiveDisposition(ctx, o, state, done); err != nil {
		t.Fatal(err)
	}
	permit, err := s.ReadReplicaPermit(ctx, sqliteArchiveDatabase(t, p.Candidate.Databases[0]).Binding.DatabaseID)
	if err != nil || permit.FenceState != "released" {
		t.Fatal("held fence after abandonment", err, permit)
	}
	retained, err := s.ReadArchiveManifest(ctx, p.ArchiveID)
	if err != nil || !reflect.DeepEqual(retained, m) {
		t.Fatal("lost preparation history", err)
	}
	if _, exists, err := s.ReadArchiveOperation(ctx, p.ID); err != nil || !exists {
		t.Fatal("released fence lost archive operation", err)
	}
	// A later removal gets a new archive identity while preserving this incarnation.
	id, err := data.NewID()
	if err != nil {
		t.Fatal(err)
	}
	p.ArchiveID = data.ArchiveID(id)
	p.Trees[0].ArchiveRelative = "archives/" + id
	p.ID, err = p.Fingerprint()
	if err != nil {
		t.Fatal(err)
	}
	if err = s.SaveArchivePlan(ctx, p); err != nil {
		t.Fatal(err)
	}
	next, err := s.ClaimArchive(ctx, p)
	if err != nil || next.ID == o.ID {
		t.Fatal("abandonment prevented a later independent attempt", err)
	}
	if _, err = s.FinishArchiveDisposition(ctx, o, state, done); !errors.Is(err, ErrConflict) {
		t.Fatal("old abandonment released new ownership", err)
	}
}
func TestArchiveRetirementPreservesEvidenceAndRecreatesFreshIdentities(t *testing.T) {
	s := openTest(t)
	p, o, _ := preparedStoredArchive(t, s)
	ctx := context.Background()
	state := archive.Disposition{}
	for _, stage := range []archive.DispositionStage{archive.CleanupIntent, archive.RuntimeRetired, archive.RenameIntent, archive.Renamed} {
		next := archive.Disposition{ArchiveID: p.ArchiveID, Sequence: state.Sequence + 1, Stage: stage}
		var err error
		state, err = s.AdvanceArchiveDisposition(ctx, o, state, next)
		if err != nil {
			t.Fatal(err)
		}
	}
	done := archive.Disposition{ArchiveID: p.ArchiveID, Sequence: state.Sequence + 1, Stage: archive.Retired, CommittedAt: time.Now().UTC()}
	done.RetainUntil = done.CommittedAt.Add(30 * 24 * time.Hour)
	if _, err := s.FinishArchiveDisposition(ctx, o, state, done); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ArchiveCandidate(ctx, p.Candidate.App); err == nil {
		t.Fatal("retired incarnation still active")
	}
	reserved, err := s.ReserveDatabase(ctx, dataRequest())
	if err != nil {
		t.Fatal(err)
	}
	old := sqliteArchiveDatabase(t, p.Candidate.Databases[0])
	if reserved.Database.IncarnationID == old.Binding.IncarnationID || reserved.Database.DatabaseID == old.Binding.DatabaseID || reserved.Replica.BindingID == old.Binding.ReplicaBindingID || reserved.Replica.EpochID == old.Epochs[0].EpochID || reserved.Replica.RemotePrefix == old.Epochs[0].RemotePrefix || reserved.Database.RelativeDirectory == old.Binding.RelativeDirectory {
		t.Fatal("reused archived identity", reserved)
	}
	if _, err = s.FinishArchiveDisposition(ctx, o, state, done); !errors.Is(err, ErrConflict) {
		t.Fatal("stale retirement reached recreation", err)
	}
	permit, err := s.ReadReplicaPermit(ctx, old.Binding.DatabaseID)
	if err != nil || permit.FenceState != "held" {
		t.Fatal("archived startup fence released", err)
	}
	generation, err := s.Generation(ctx)
	if err != nil || generation != 1 {
		t.Fatal("retirement missing from generation", generation, err)
	}
	for _, statement := range []string{"DELETE FROM data_archive_dispositions", "DELETE FROM data_archive_retirements"} {
		if _, err = s.db.ExecContext(ctx, statement); err == nil {
			t.Fatal("mutable retirement history", statement)
		}
	}
}
func TestArchiveMigrationTwelvePreservesPreparedManifestAndFences(t *testing.T) {
	s := openTest(t)
	p, o, m := preparedStoredArchive(t, s)
	ctx := context.Background()
	dropEngineMigrationFixture(t, ctx, s)
	for _, table := range []string{"data_archive_credential_receipts", "data_archive_credential_plans", "data_archive_retention_proofs", "data_archive_deletions", "data_archive_maintenance_plans", "data_archive_retirements", "data_archive_dispositions"} {
		if _, err := s.db.ExecContext(ctx, "DROP TABLE "+table); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.db.ExecContext(ctx, "UPDATE schema_version SET version=12"); err != nil {
		t.Fatal(err)
	}
	if err := s.migrate(ctx); err != nil {
		t.Fatal(err)
	}
	actual, err := s.ReadArchiveManifest(ctx, p.ArchiveID)
	if err != nil || !reflect.DeepEqual(actual, m) {
		t.Fatal("migration replaced immutable manifest", err)
	}
	read, exists, err := s.ReadArchiveOperation(ctx, p.ID)
	if err != nil || !exists || !reflect.DeepEqual(read, o) {
		t.Fatal("migration lost operation/fence history", err)
	}
	var violations int
	rows, err := s.db.QueryContext(ctx, "PRAGMA foreign_key_check")
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		violations++
	}
	if err = rows.Close(); err != nil || violations != 0 {
		t.Fatal("migration damaged references", violations, err)
	}
	raw, err := json.Marshal(actual)
	if err != nil || len(raw) == 0 {
		t.Fatal(err)
	}
}

func TestArchivePublishedRetirementReleasesHeadAndInventoryForRecreation(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	p := storedArchivePlan(t, s)
	req := dataRequest()
	in := fixture(t)
	in.Desired.Name = spec.Name(req.App)
	in.Desired.Runtime = &data.RuntimeIdentity{UID: 10001, GID: 10001}
	in.Desired.Databases = []data.SQLiteDatabase{req.Database}
	in.Desired.SchemaCompatibility = []data.SQLiteSchemaCompatibility{{Database: "main", Startup: "preserve", Accepts: []string{data.EmptyMarker}}}
	planned, err := plan.Build(in)
	if err != nil {
		t.Fatal(err)
	}
	planID, err := s.SavePlan(ctx, planned, in.Desired)
	if err != nil {
		t.Fatal(err)
	}
	release := Release{ID: "published-release", PlanID: planID, Image: planned.Image, HostPort: planned.HostPort, Secrets: planned.Secrets, Units: []target.Unit{{Name: req.App + ".container", Hash: "sha256:" + strings.Repeat("b", 64)}}, CaddyFile: target.CaddyFile{Name: req.App + ".caddy", Hash: "sha256:" + strings.Repeat("c", 64)}}
	if err = s.CommitRelease(ctx, req.App, release); err != nil {
		t.Fatal(err)
	}
	p.Candidate.ReleaseID = release.ID
	p.Generation = 1
	id, err := data.NewID()
	if err != nil {
		t.Fatal(err)
	}
	p.ArchiveID = data.ArchiveID(id)
	p.Trees[0].ArchiveRelative = "archives/" + id
	p.ID, err = p.Fingerprint()
	if err != nil {
		t.Fatal(err)
	}
	if err = s.SaveArchivePlan(ctx, p); err != nil {
		t.Fatal(err)
	}
	p, o, _ := preparedStoredArchive(t, s, p)
	if _, err = s.ReadWriterSchema(ctx, p.Candidate.Incarnation); !errors.Is(err, ErrConflict) {
		t.Fatal("ordinary writer bypassed archive fence", err)
	}
	if _, err = s.ReadArchiveWriterSchema(ctx, p.Candidate.Incarnation, o.ID); err != nil {
		t.Fatal("owner could not reverify declarations", err)
	}
	state := archive.Disposition{}
	for _, stage := range []archive.DispositionStage{archive.CleanupIntent, archive.RuntimeRetired, archive.RenameIntent, archive.Renamed} {
		next := archive.Disposition{ArchiveID: p.ArchiveID, Sequence: state.Sequence + 1, Stage: stage}
		state, err = s.AdvanceArchiveDisposition(ctx, o, state, next)
		if err != nil {
			t.Fatal(err)
		}
	}
	done := archive.Disposition{ArchiveID: p.ArchiveID, Sequence: state.Sequence + 1, Stage: archive.Retired, CommittedAt: time.Now().UTC()}
	done.RetainUntil = done.CommittedAt.Add(30 * 24 * time.Hour)
	if _, err = s.FinishArchiveDisposition(ctx, o, state, done); err != nil {
		t.Fatal(err)
	}
	if _, err = s.CurrentRelease(ctx, req.App); !errors.Is(err, ErrNotFound) {
		t.Fatal("retired head retained", err)
	}
	inventory, err := s.InventoryState(ctx)
	if err != nil || len(inventory.Apps) != 1 || inventory.Apps[0].Status != target.Absent || len(inventory.Apps[0].RetiredPorts) != 1 || inventory.Generation != 2 {
		t.Fatal("retirement inventory not settled", inventory, err)
	}
	next, err := s.ReserveDatabase(ctx, req)
	if err != nil || next.Database.IncarnationID == p.Candidate.Incarnation {
		t.Fatal("recreation reused incarnation", err)
	}
	if _, err = s.ReleaseByID(ctx, req.App, release.ID); err != nil {
		t.Fatal("release history lost", err)
	}
}

func TestArchiveAbandonEveryStoredPreManifestBoundary(t *testing.T) {
	for sequence := uint64(1); sequence < 8; sequence++ {
		stage, _ := archive.StageAt(sequence)
		t.Run(string(stage), func(t *testing.T) {
			ctx := context.Background()
			s := openTest(t)
			p := storedArchivePlan(t, s)
			o, err := s.ClaimArchive(ctx, p)
			if err != nil {
				t.Fatal(err)
			}
			for o.Sequence < sequence {
				next, _ := archive.StageAt(o.Sequence + 1)
				if next == archive.CoverageVerified {
					d := sqliteArchiveDatabase(t, p.Candidate.Databases[0])
					fresh := *d.Allocation
					fresh.AllocatedAt = time.Now().UTC()
					coverage := []archive.Coverage{archive.SQLiteCoverageVariant(archive.SQLiteCoverage{DatabaseID: d.Binding.DatabaseID, WritersQuiescedAt: fresh.AllocatedAt, Empty: &fresh, Observation: data.SQLiteSchemaObservation{DatabaseID: d.Binding.DatabaseID, State: data.AllocatedEmpty, Marker: data.EmptyMarker, CatalogSHA256: data.EmptyCatalogSHA256, ObservedAt: fresh.AllocatedAt}})}
					o, err = s.SaveArchiveCoverage(ctx, o, coverage)
				} else {
					o, err = s.AdvanceArchive(ctx, o, next)
				}
				if err != nil {
					t.Fatal(err)
				}
			}
			before := o
			before.Fences = append([]data.QuiescenceFence(nil), o.Fences...)
			for i := range before.Fences {
				before.Fences[i].State = data.FenceReleased
			}
			request := dataRequest()
			request.App = "other"
			unrelated, err := s.ReserveDatabase(ctx, request)
			if err != nil {
				t.Fatal(err)
			}
			foreign, err := s.HoldDataFence(ctx, unrelated.Database.DatabaseID, "other-operation")
			if err != nil {
				t.Fatal(err)
			}
			retained, err := s.LoadArchivePlanByArchive(ctx, p.ArchiveID)
			if err != nil || !reflect.DeepEqual(retained, p) {
				t.Fatal("lost immutable plan", err)
			}
			intent := archive.Disposition{ArchiveID: p.ArchiveID, Sequence: 1, Stage: archive.AbandonIntent}
			intent, err = s.AdvanceArchiveDisposition(ctx, o, archive.Disposition{}, intent)
			if err != nil {
				t.Fatal(err)
			}
			// An interrupted abandonment resumes the same intent, never preparation.
			read, exists, err := s.ReadArchiveDisposition(ctx, p.ArchiveID)
			if err != nil || !exists || !reflect.DeepEqual(read, intent) {
				t.Fatal(err, read)
			}
			done := archive.Disposition{ArchiveID: p.ArchiveID, Sequence: 2, Stage: archive.Abandoned, CommittedAt: time.Now().UTC()}
			wrong := o
			wrong.ID = strings.Repeat("a", 32)
			if _, err = s.FinishArchiveDisposition(ctx, wrong, intent, done); !errors.Is(err, ErrConflict) {
				t.Fatal("foreign operation released fences", err)
			}
			own, err := s.ReadReplicaPermit(ctx, sqliteArchiveDatabase(t, p.Candidate.Databases[0]).Binding.DatabaseID)
			if err != nil || own.FenceState != "held" {
				t.Fatal("failed transaction changed fence", err)
			}
			if _, err = s.FinishArchiveDisposition(ctx, o, intent, done); err != nil {
				t.Fatal(err)
			}
			own, err = s.ReadReplicaPermit(ctx, sqliteArchiveDatabase(t, p.Candidate.Databases[0]).Binding.DatabaseID)
			if err != nil || own.FenceState != "released" {
				t.Fatal("owned fence not released", err)
			}
			other, err := s.ReadReplicaPermit(ctx, unrelated.Database.DatabaseID)
			if err != nil || other.FenceState != "held" || len(other.Fences) != 1 || other.Fences[0] != foreign {
				t.Fatal("foreign fence changed", err)
			}
			after, exists, err := s.ReadArchiveOperation(ctx, p.ID)
			if err != nil || !exists || !reflect.DeepEqual(after, before) {
				t.Fatal("preparation history changed", err)
			}
			if _, err = s.ReadArchiveManifest(ctx, p.ArchiveID); err == nil {
				t.Fatal("invented manifest")
			}
			if _, err = s.ArchiveCandidate(ctx, p.Candidate.App); err != nil {
				t.Fatal("app remains blocked", err)
			}
		})
	}
}
