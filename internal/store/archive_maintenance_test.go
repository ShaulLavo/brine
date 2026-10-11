//go:build linux

package store

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/ShaulLavo/brine/internal/archive"
	"github.com/ShaulLavo/brine/internal/data"
)

func storedMaintenancePlan(t *testing.T, s *Store) (archive.MaintenancePlan, archive.Plan, archive.Disposition) {
	t.Helper()
	ctx := context.Background()
	p, o, _ := preparedStoredArchive(t, s)
	state := archive.Disposition{}
	for _, stage := range []archive.DispositionStage{archive.CleanupIntent, archive.RuntimeRetired, archive.RenameIntent, archive.Renamed} {
		next := archive.Disposition{ArchiveID: p.ArchiveID, Sequence: state.Sequence + 1, Stage: stage}
		var err error
		state, err = s.AdvanceArchiveDisposition(ctx, o, state, next)
		if err != nil {
			t.Fatal(err)
		}
	}
	d := archive.Disposition{ArchiveID: p.ArchiveID, Sequence: state.Sequence + 1, Stage: archive.Retired, CommittedAt: time.Now().UTC()}
	d.RetainUntil = d.CommittedAt.Add(30 * 24 * time.Hour)
	if _, err := s.FinishArchiveDisposition(ctx, o, state, d); err != nil {
		t.Fatal(err)
	}
	m := archive.MaintenancePlan{ArchiveID: p.ArchiveID, ArchivePlanID: p.ID, Kind: archive.PurgeArchive, Requester: p.Requester, PolicyHash: p.PolicyHash, TargetHash: p.TargetHash, CreatedAt: time.Now().UTC(), RetiredAt: d.CommittedAt, RetainUntil: d.RetainUntil}
	for _, tree := range p.Trees {
		m.Parents = append(m.Parents, data.ArchiveDeletionParent{Root: tree.Root, Device: tree.RootDevice, Inode: tree.RootInode})
	}
	var err error
	m.ID, err = m.Fingerprint()
	if err != nil {
		t.Fatal(err)
	}
	if err = s.SaveMaintenancePlan(ctx, m); err != nil {
		t.Fatal(err)
	}
	return m, p, d
}

func TestArchiveMaintenanceJournalSurvivesReopenAndRejectsStaleCAS(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	m, p, d := storedMaintenancePlan(t, s)
	proof := archive.RetentionProof{PolicyHash: p.PolicyHash, TargetHash: p.TargetHash, ArchiveID: p.ArchiveID, VerifiedAt: time.Now().UTC(), RetainUntil: d.RetainUntil}
	proof.ValidUntil = proof.VerifiedAt.Add(archive.MaxEvidenceAge)
	if err := s.SaveRetentionProof(ctx, proof); err != nil {
		t.Fatal(err)
	}
	intent := archive.Deletion{ArchiveID: p.ArchiveID, PlanID: m.ID, Kind: m.Kind, Sequence: 1, Stage: archive.DeletionIntent, StartedAt: time.Now().UTC()}
	if err := s.AdvanceArchiveDeletion(ctx, archive.Deletion{}, intent); err != nil {
		t.Fatal(err)
	}
	if err := s.AdvanceArchiveDeletion(ctx, archive.Deletion{}, intent); !errors.Is(err, ErrConflict) {
		t.Fatal("stale deletion claim admitted", err)
	}
	if err := s.SaveRetentionProof(ctx, proof); !errors.Is(err, ErrConflict) {
		t.Fatal("retention renewed after deletion started", err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(s.dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := reopened.Close(); err != nil {
			t.Error(err)
		}
	}()
	if got, err := reopened.LoadMaintenancePlan(ctx, m.ID); err != nil || !reflect.DeepEqual(got, m) {
		t.Fatal("reviewed plan lost", got, err)
	}
	if got, err := reopened.ReadArchiveDeletion(ctx, p.ArchiveID); err != nil || got != intent {
		t.Fatal("durable deletion intent lost", got, err)
	}
	if got, err := reopened.ReadRetentionProof(ctx, p.ArchiveID); err != nil || !reflect.DeepEqual(got, proof) {
		t.Fatal("historical retention proof lost", got, err)
	}
	for _, statement := range []string{"DELETE FROM data_archive_maintenance_plans", "DELETE FROM data_archive_deletions", "DELETE FROM data_archive_retention_proofs"} {
		if _, err := reopened.db.ExecContext(ctx, statement); err == nil {
			t.Fatal("maintenance history mutable", statement)
		}
	}
}

func TestArchiveMaintenanceRejectsChangedDeadlineAndEarlyExpiry(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	m, p, _ := storedMaintenancePlan(t, s)
	changed := m
	changed.RetainUntil = changed.RetainUntil.Add(time.Hour)
	var err error
	changed.ID, err = changed.Fingerprint()
	if err != nil {
		t.Fatal(err)
	}
	if err = s.SaveMaintenancePlan(ctx, changed); err == nil {
		t.Fatal("retention deadline rewritten")
	}
	m.Kind = archive.ExpireArchive
	m.ID, err = m.Fingerprint()
	if err != nil {
		t.Fatal(err)
	}
	if err = s.SaveMaintenancePlan(ctx, m); err != nil {
		t.Fatal(err)
	}
	intent := archive.Deletion{ArchiveID: p.ArchiveID, PlanID: m.ID, Kind: m.Kind, Sequence: 1, Stage: archive.DeletionIntent, StartedAt: time.Now().UTC()}
	if err = s.AdvanceArchiveDeletion(ctx, archive.Deletion{}, intent); !errors.Is(err, ErrConflict) {
		t.Fatal("early expiry journaled", err)
	}
	if got, err := s.ReadArchiveDeletion(ctx, p.ArchiveID); err != nil || got.Sequence != 0 {
		t.Fatal("rejected expiry left authority", got, err)
	}
}

func TestArchiveMaintenanceRejectsNoncanonicalJournalEvidence(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	m, p, _ := storedMaintenancePlan(t, s)
	reviewed := m
	reviewed.Kind = archive.VerifyRetention
	var err error
	reviewed.ID, err = reviewed.Fingerprint()
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(reviewed)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.ExecContext(ctx, "INSERT INTO data_archive_maintenance_plans VALUES(?,?,?)", reviewed.ID, reviewed.ArchiveID, append([]byte(" "), raw...)); err != nil {
		t.Fatal(err)
	}
	var integrity *IntegrityError
	if _, err = s.LoadMaintenancePlan(ctx, reviewed.ID); !errors.As(err, &integrity) {
		t.Fatal("noncanonical review admitted", err)
	}
	intent := archive.Deletion{ArchiveID: p.ArchiveID, PlanID: m.ID, Kind: m.Kind, Sequence: 1, Stage: archive.DeletionIntent, StartedAt: time.Now().UTC()}
	raw, err = json.Marshal(intent)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.ExecContext(ctx, "INSERT INTO data_archive_deletions VALUES(?,?,?,?)", intent.ArchiveID, intent.Sequence, intent.PlanID, append([]byte(" "), raw...)); err != nil {
		t.Fatal(err)
	}
	if _, err = s.ReadArchiveDeletion(ctx, p.ArchiveID); !errors.As(err, &integrity) {
		t.Fatal("noncanonical deletion authority admitted", err)
	}
}
