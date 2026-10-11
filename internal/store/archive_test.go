//go:build linux

package store

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/ShaulLavo/brine/internal/archive"
	"github.com/ShaulLavo/brine/internal/data"
)

func storedArchivePlan(t *testing.T, s *Store) archive.Plan {
	t.Helper()
	ctx := context.Background()
	req := dataRequest()
	r, err := s.ReserveDatabase(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	a := data.AllocationReceipt{DatabaseID: r.Database.DatabaseID, IncarnationID: r.Database.IncarnationID, RootDevice: 1, RootInode: 2, DirectoryDevice: 1, DirectoryInode: 3, AllocatedAt: now.Add(-time.Minute)}
	if err = s.RecordAllocation(ctx, a); err != nil {
		t.Fatal(err)
	}
	c, err := s.ArchiveCandidate(ctx, req.App)
	if err != nil {
		t.Fatal(err)
	}
	c.Databases[0].Retention = []data.RetentionEvidence{{Destination: req.Destination.Reference, Endpoint: req.Destination.Endpoint, Bucket: req.Destination.Bucket, BasePrefix: req.Destination.BasePrefix, VerificationID: strings.Repeat("f", 32), VerifiedAt: now.Format(time.RFC3339Nano), FreshnessSeconds: 3600, NoObjectExpiration: true}}
	id, err := data.NewID()
	if err != nil {
		t.Fatal(err)
	}
	p := archive.Plan{ArchiveID: data.ArchiveID(id), Requester: "deploy:" + strings.Repeat("a", 64), PolicyHash: req.PolicyHash, TargetHash: "sha256:" + strings.Repeat("b", 64), CreatedAt: now, Candidate: c, Trees: []data.ArchiveTree{{Root: r.Database.Root, RootDevice: 1, RootInode: 2, SourceDevice: 1, SourceInode: 4, SourceRelative: "apps/" + string(c.Incarnation), ArchiveRelative: "archives/" + id}}}
	p.ID, err = p.Fingerprint()
	if err != nil || !p.Valid() {
		t.Fatal("invalid plan", err)
	}
	if err = s.SaveArchivePlan(ctx, p); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestArchiveJournalPreparationAndImmutableHistory(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	p := storedArchivePlan(t, s)
	o, err := s.ClaimArchive(ctx, p)
	if err != nil {
		t.Fatal(err)
	}
	permit, err := s.ReadReplicaPermit(ctx, p.Candidate.Databases[0].Binding.DatabaseID)
	if err != nil || permit.FenceState != "held" {
		t.Fatal("no durable fence", permit, err)
	}
	if _, err = s.ClaimArchive(ctx, p); err == nil {
		t.Fatal("second attempt claimed")
	}
	stale := o
	if _, err = s.AdvanceArchive(ctx, o, archive.CoverageIntent); !errors.Is(err, ErrConflict) {
		t.Fatal("skipped boundary admitted", err)
	}
	for _, stage := range []archive.Stage{archive.WritersStopIntent, archive.WritersStopped, archive.CoverageIntent} {
		o, err = s.AdvanceArchive(ctx, o, stage)
		if err != nil {
			t.Fatal(err)
		}
	}
	if _, err = s.AdvanceArchive(ctx, stale, archive.WritersStopIntent); !errors.Is(err, ErrConflict) {
		t.Fatal("stale operation admitted", err)
	}
	d := p.Candidate.Databases[0]
	now := time.Now().UTC()
	fresh := *d.Allocation
	fresh.AllocatedAt = now
	coverage := []archive.Coverage{{DatabaseID: d.Binding.DatabaseID, WritersQuiescedAt: now, Empty: &fresh, Observation: data.SchemaObservation{DatabaseID: d.Binding.DatabaseID, State: data.AllocatedEmpty, Marker: data.EmptyMarker, CatalogSHA256: data.EmptyCatalogSHA256, ObservedAt: now}}}
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
	read, exists, err := s.ReadArchiveOperation(ctx, p.ID)
	if err != nil || !exists || !reflect.DeepEqual(o, read) {
		t.Fatal("operation changed", err)
	}
	restored, err := s.ReadArchiveManifest(ctx, p.ArchiveID)
	if err != nil || !reflect.DeepEqual(restored, m) {
		t.Fatal("manifest changed", err)
	}
	for _, table := range []string{"data_archive_plans", "data_archive_operations", "data_archive_fences", "data_archive_events", "data_archive_manifests"} {
		for _, statement := range []string{"DELETE FROM " + table, "UPDATE " + table + " SET " + map[string]string{"data_archive_plans": "id=id", "data_archive_operations": "id=id", "data_archive_fences": "fence_id=fence_id", "data_archive_events": "stage=stage", "data_archive_manifests": "archive_id=archive_id"}[table]} {
			if _, err = s.db.ExecContext(ctx, statement); err == nil {
				t.Fatalf("history mutable: %s", statement)
			}
		}
	}
	if _, err = s.ArchiveCandidate(ctx, p.Candidate.App); !errors.Is(err, ErrConflict) {
		t.Fatal("fenced data became eligible again", err)
	}
	var active string
	if err = s.db.QueryRowContext(ctx, "SELECT incarnation_id FROM data_active_incarnations WHERE app=?", p.Candidate.App).Scan(&active); err != nil || active != string(p.Candidate.Incarnation) {
		t.Fatal("preparation retired incarnation", err)
	}
}
func TestArchiveCandidateRefusesUnsettledInitialization(t *testing.T) {
	s := openTest(t)
	p, _ := storeInitPlan(t, s)
	if _, err := s.ClaimInitialization(context.Background(), p); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ArchiveCandidate(context.Background(), p.Request.App); !errors.Is(err, ErrConflict) {
		t.Fatal("unsettled initialization admitted", err)
	}
}
func TestArchiveManifestCannotReplaceVerifiedCoverage(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	p := storedArchivePlan(t, s)
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
	d := p.Candidate.Databases[0]
	now := time.Now().UTC()
	fresh := *d.Allocation
	fresh.AllocatedAt = now
	coverage := []archive.Coverage{{DatabaseID: d.Binding.DatabaseID, WritersQuiescedAt: now, Empty: &fresh, Observation: data.SchemaObservation{DatabaseID: d.Binding.DatabaseID, State: data.AllocatedEmpty, Marker: data.EmptyMarker, CatalogSHA256: data.EmptyCatalogSHA256, ObservedAt: now}}}
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
	raw, _ := json.Marshal(coverage)
	coverage = nil
	if err = json.Unmarshal(raw, &coverage); err != nil {
		t.Fatal(err)
	}
	coverage[0].Observation.ObservedAt = now.Add(time.Nanosecond)
	m := archive.Manifest{Plan: p, OperationID: o.ID, Coverage: coverage, QuiescedAt: time.Now().UTC(), PreparedAt: time.Now().UTC()}
	if _, err = s.SaveArchiveManifest(ctx, o, m); !errors.Is(err, ErrConflict) {
		t.Fatal("changed coverage admitted", err)
	}
}

func dropArchiveMigrationFixture(t *testing.T, ctx context.Context, s *Store) {
	t.Helper()
	for _, table := range []string{"data_archive_credential_receipts", "data_archive_credential_plans", "data_archive_retention_proofs", "data_archive_deletions", "data_archive_maintenance_plans", "data_archive_retirements", "data_archive_dispositions", "data_archive_manifests", "data_archive_events", "data_archive_fences", "data_archive_operations", "data_archive_plans"} {
		if _, err := s.db.ExecContext(ctx, "DROP TABLE "+table); err != nil {
			t.Fatal(err)
		}
	}
}
func TestArchiveMigrationFromSchemaTenPreservesAllocations(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	p := storedArchivePlan(t, s)
	dropArchiveMigrationFixture(t, ctx, s)
	for _, table := range []string{"data_init_reviews", "data_init_artifacts"} {
		if _, err := s.db.ExecContext(ctx, "DROP TABLE "+table); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.db.ExecContext(ctx, "UPDATE schema_version SET version=10"); err != nil {
		t.Fatal(err)
	}
	if err := s.migrate(ctx); err != nil {
		t.Fatal(err)
	}
	candidate, err := s.ArchiveCandidate(ctx, p.Candidate.App)
	if err != nil || candidate.Incarnation != p.Candidate.Incarnation || candidate.Databases[0].Allocation == nil {
		t.Fatal("migration lost allocation", err)
	}
	if err = s.SaveArchivePlan(ctx, p); err != nil {
		t.Fatal("archive table unavailable", err)
	}
}
