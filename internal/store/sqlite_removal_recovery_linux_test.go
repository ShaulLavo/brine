//go:build linux

package store

import (
	"bytes"
	"reflect"
	"testing"
	"time"

	"github.com/ShaulLavo/brine/internal/archive"
	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/ops"
	"github.com/ShaulLavo/brine/internal/policy"
)

func (r *removalFixture) recovering(o archive.Operation) {
	r.t.Helper()
	if err := r.fx.s.MarkSQLiteRetireRecovery(r.t.Context(), r.task, "interrupted"); err != nil {
		r.t.Fatal(err)
	}
	r.task = r.createTask(ops.SQLiteRetireResolve, r.task.ID, "continue")
	r.preflight()
}

// Fake isolated-restore output for real protected-record tests. This is not a
// physical restore or provider-retention acceptance claim.
func (r *removalFixture) recoveryEvidence() []SQLiteRemovalRecoveryEvidence {
	evidence := []SQLiteRemovalRecoveryEvidence{}
	for _, variant := range r.plan.Candidate.Databases {
		d, _ := variant.SQLite()
		if !d.Written {
			continue
		}
		point, _, err := r.fx.s.ReadSQLiteRemovalPoint(r.t.Context(), r.plan.ID, d.Binding.DatabaseID)
		if err != nil {
			r.t.Fatal(err)
		}
		receipt := point.Verification
		receipt.OperationID = r.task.ID + "-" + string(d.Binding.DatabaseID)
		receipt.ObservedAt = r.fx.s.removalNow()
		receipt.LossWindow.To = receipt.ObservedAt
		evidence = append(evidence, SQLiteRemovalRecoveryEvidence{DatabaseID: d.Binding.DatabaseID, Verification: receipt})
	}
	return evidence
}

func TestSQLiteRemovalRecoveryFreshOriginalSourceAfterPointExpiry(t *testing.T) {
	r := newRemovalFixture(t, false)
	o := r.preparedArchive(r.claim())
	original := r.fx.preparation(r.plan.Removal.Points[0].PreparationID)
	r.recovering(o)
	at := r.plan.Removal.Points[0].ExpiresAt.Add(time.Second)
	r.fx.s.removalClock = func() time.Time { return at }
	if err := r.fx.s.RecordSQLiteRemovalRecovery(t.Context(), r.task.ID, o, r.recoveryEvidence(), r.guard); err != nil {
		t.Fatal("fresh original-source recovery", err)
	}
	receipt := r.retire(o)
	r.assertSuccess(receipt)
	retained := r.fx.preparation(r.plan.Removal.Points[0].PreparationID)
	if !reflect.DeepEqual(original.point, retained.point) {
		t.Fatal("recovery relabeled immutable source times or point")
	}
}

func TestSQLiteRemovalRecoveryWholeSetAndSourceRefusals(t *testing.T) {
	for _, name := range []string{"missing", "extra", "duplicate", "crossed_database", "crossed_task", "source", "schema", "cutoff", "future", "old", "deadline"} {
		t.Run(name, func(t *testing.T) { checkRemovalRecoveryNegative(t, name) })
	}
}

func checkRemovalRecoveryNegative(t *testing.T, name string) {
	r := newRemovalFixture(t, true)
	o := r.preparedArchive(r.claim())
	r.recovering(o)
	evidence := r.recoveryEvidence()
	task := r.task.ID
	switch name {
	case "missing":
		evidence = evidence[:1]
	case "extra", "duplicate":
		evidence = append(evidence, evidence[0])
	case "crossed_database":
		evidence[0].DatabaseID = evidence[1].DatabaseID
	case "crossed_task":
		task = r.fx.preparation(r.plan.Removal.Points[0].PreparationID).intent.Task.OperationID
	case "source":
		evidence[0].Verification.Source.LTX.TXID++
	case "schema":
		evidence[0].Verification.Schema.Marker = "other"
	case "cutoff":
		crossed := *evidence[0].Verification.Coverage
		crossed.CoveredThrough = crossed.CoveredThrough.Add(time.Second)
		evidence[0].Verification.Coverage = &crossed
	case "future":
		evidence[0].Verification.ObservedAt = time.Now().UTC().Add(time.Hour)
	case "old":
		evidence[0].Verification.ObservedAt = time.Now().UTC().Add(-time.Hour)
	case "deadline":
		after := r.plan.CreatedAt.Add(25 * time.Hour)
		r.fx.s.removalClock = func() time.Time { return after }
		evidence = r.recoveryEvidence()
	}
	if err := r.fx.s.RecordSQLiteRemovalRecovery(t.Context(), task, o, evidence, r.guard); err == nil {
		t.Fatal("crossed recovery admitted", name)
	}
	if n := r.fx.count("SELECT count(*) FROM data_sqlite_archive_recovery"); n != 0 {
		t.Fatal("partial recovery evidence committed", n)
	}
}

func TestSQLiteRemovalRecoveryLateTerminalClockRefuses(t *testing.T) {
	r := newRemovalFixture(t, false)
	o := r.preparedArchive(r.claim())
	r.recovering(o)
	if err := r.fx.s.RecordSQLiteRemovalRecovery(t.Context(), r.task.ID, o, r.recoveryEvidence(), r.guard); err != nil {
		t.Fatal(err)
	}
	at := time.Now().UTC().Add(6 * time.Minute)
	r.fx.s.removalClock = func() time.Time { return at }
	old := removalReadyToFinish(t, r, o)
	_, _, err := r.fx.s.FinishRemoval(t.Context(), r.task.ID, o, old, archive.Disposition{ArchiveID: r.plan.ArchiveID, Sequence: old.Sequence + 1, Stage: archive.Retired}, r.guard)
	if err == nil || r.fx.count("SELECT count(*) FROM data_sqlite_archive_completions") != 0 {
		t.Fatal("late terminal completion used stale recovery evidence", err)
	}
}

func TestSQLiteRemovalRecoveryProtectedPolicyRetentionRefresh(t *testing.T) {
	r := newRemovalFixture(t, false)
	o := r.preparedArchive(r.claim())
	before := r.fx.preparation(r.plan.Removal.Points[0].PreparationID)
	r.recovering(o)
	if err := r.guard.Close(); err != nil {
		t.Fatal(err)
	}
	raw := bytes.Replace(r.policyRaw, []byte("operator-1"), []byte("operator-2"), 1)
	evidence, _ := r.policy.BackupRetention("primary")
	freshID, _ := data.NewID()
	raw = bytes.Replace(raw, []byte(evidence.VerificationID), []byte(freshID), 1)
	raw = bytes.Replace(raw, []byte(evidence.VerifiedAt), []byte(time.Now().UTC().Format(time.RFC3339Nano)), 1)
	refreshed, err := policy.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	r.policy = refreshed
	r.readmit(r.fx)
	r.hosts = nil
	r.hold()
	if err = r.fx.s.RecordSQLiteRemovalRecovery(t.Context(), r.task.ID, o, r.recoveryEvidence(), r.guard); err != nil {
		t.Fatal("same-source refreshed policy evidence", err)
	}
	receipt := r.retire(o)
	r.assertSuccess(receipt)
	after := r.fx.preparation(r.plan.Removal.Points[0].PreparationID)
	if !reflect.DeepEqual(before.point, after.point) || !reflect.DeepEqual(before.intent, after.intent) {
		t.Fatal("refresh relabeled point, scope or original cutoff")
	}
}

func TestSQLiteRemovalRecoveryReadbackRejectsCorruptProtectedProof(t *testing.T) {
	r := newRemovalFixture(t, false)
	o := r.preparedArchive(r.claim())
	r.recovering(o)
	if err := r.fx.s.RecordSQLiteRemovalRecovery(t.Context(), r.task.ID, o, r.recoveryEvidence(), r.guard); err != nil {
		t.Fatal(err)
	}
	r.retire(o)
	execRemovalFixture(t, r.fx.s, "DROP TRIGGER data_sqlite_archive_recovery_no_update")
	if _, err := r.fx.s.db.ExecContext(t.Context(), "UPDATE data_sqlite_archive_recovery SET canonical=json_set(canonical,'$.evidence[0].verification.schema.marker','crossed')"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := r.fx.s.ReadSQLiteRetireSuccess(t.Context(), r.task); err == nil {
		t.Fatal("committed-success reader trusted corrupt restore proof")
	}
	if _, err := readSQLitePreparation(t.Context(), r.fx.s.db, r.plan.Removal.Points[0].PreparationID); err == nil {
		t.Fatal("preparation history trusted corrupt completion proof")
	}
}
