//go:build linux

package store

import (
	"bytes"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/ShaulLavo/brine/internal/archive"
	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/ops"
)

func TestNativeArchiveConsumerUsesPreparedGeneration(t *testing.T) {
	testNativeArchiveClaim(t, false)
}

func TestNativeArchiveClaimsAbandonedIncomingTree(t *testing.T) {
	testNativeArchiveClaim(t, true)
}

func testNativeArchiveClaim(t *testing.T, abandoned bool) (*sql.Tx, archive.Plan, archive.Operation, archive.NativeDatabase) {
	t.Helper()
	s, prepared, point := nativeCompletedPointFixture(t)
	ctx := t.Context()
	original, err := s.GetOperation(ctx, prepared.intent.OperationID)
	if err != nil {
		t.Fatal(err)
	}
	head, _, err := s.ReadEngineHead(ctx, point.BindingID)
	if err != nil {
		t.Fatal(err)
	}
	p := archive.Plan{ID: "sha256:" + strings.Repeat("a", 64), ArchiveID: data.ArchiveID(strings.Repeat("b", 32)), Requester: original.Requester, PolicyHash: "sha256:" + point.PolicySHA256, Generation: point.Generation + 10, Candidate: archive.Candidate{App: original.App, Incarnation: point.IncarnationID}}
	o := archive.Operation{ID: strings.Repeat("c", 32), PlanID: p.ID, ArchiveID: p.ArchiveID}
	d := archive.NativeDatabase{Head: head, Binding: data.SpacetimeDBBinding{BindingID: point.BindingID, DatabaseID: point.DatabaseID, IncarnationID: point.IncarnationID}, Dependencies: []data.EngineEvidenceRef{{Kind: data.EnginePreparedPointEvidence, ID: point.PointID, SHA256: nativePointHash(abandonJSON(t, point)), DatabaseID: point.DatabaseID, BindingID: point.BindingID, EpochID: point.EpochID}}}
	if abandoned {
		incoming := strings.Repeat("d", 32)
		abandonExec(t, s.db, "INSERT INTO data_native_repository_intents VALUES(?,?,?,?,?,?,?,?)", incoming, prepared.intent.OperationID, point.BindingID, strings.Repeat("e", 32), "endpoint", "bucket", "incoming", []byte(`{}`))
		d.TransitionTrees = []archive.NativeTransitionTree{{TransitionID: incoming, Stage: data.EngineTransitionAbandoned, Optional: true}}
	}
	abandonExec(t, s.db, "INSERT INTO data_archive_plans VALUES(?,?,?)", p.ID, p.ArchiveID, []byte(`{}`))
	abandonExec(t, s.db, "INSERT INTO data_archive_operations VALUES(?,?,?)", o.ID, p.ID, point.IncarnationID)
	abandonExec(t, s.db, "INSERT INTO operations(id,requester,idempotency_key,state,created_at,updated_at,kind,app,secret_ref) VALUES(?,?,?,'preflight',?,?,?,?,?)", "retire-generation", p.Requester, "retire-generation", timestamp(), timestamp(), ops.NativeRetire, p.Candidate.App, p.ID)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = tx.Rollback() })
	loaded, err := readNativePreparedPointTx(ctx, tx, point.PointID)
	if err != nil {
		t.Fatal("fixture prepared point", err)
	}
	if err := checkNativePointCurrent(ctx, tx, loaded.view); err != nil {
		t.Fatal("fixture point is not current", err)
	}
	if loaded.view.RuntimeVersion != head.Version || loaded.view.RuntimeSHA256 != head.RuntimeSHA256 || loaded.view.EpochID != head.EpochID || p.PolicyHash != "sha256:"+loaded.view.PolicySHA256 {
		t.Fatal("fixture archive head or policy differs", head, loaded.view.RuntimeVersion, loaded.view.RuntimeSHA256, loaded.view.EpochID, p.PolicyHash, loaded.view.PolicySHA256)
	}
	fence, err := s.claimNativeArchiveDatabase(ctx, tx, p, o, d, prepared.preparation.guard)
	if err != nil {
		t.Fatal("archive claim refused current coverage or retained incoming tree", err)
	}
	abandonExec(t, tx, "INSERT INTO data_archive_fences VALUES(?,?,?)", o.ID, d.Binding.DatabaseID, fence.ID)
	if actual, err := checkNativeArchivedClaimTx(ctx, tx, p, o, d); err != nil || actual != point.PointID {
		t.Fatal("accepted claim lost its original prepared generation on read", actual, err)
	}
	return tx, p, o, d
}

// Alter only disposable fixture rows after disabling their immutability guards.
// Matching canonical ledgers still cannot substitute host inventory generation
// for the verified point's original data generation.
func TestNativeArchiveConsumerRejectsWrongSourceGeneration(t *testing.T) {
	tx, p, o, d := testNativeArchiveClaim(t, false)
	ctx := t.Context()
	point, err := readNativePreparedPointTx(ctx, tx, d.Dependencies[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	original, err := readNativePointConsumer(ctx, tx, point.view.PointID)
	if err != nil {
		t.Fatal(err)
	}
	changed := bytes.Replace(original, []byte(fmt.Sprintf("\"generation\":%d", point.view.Generation)), []byte(fmt.Sprintf("\"generation\":%d", p.Generation)), 1)
	if bytes.Equal(original, changed) {
		t.Fatal("fixture generations did not differ")
	}
	abandonExec(t, tx, "DROP TRIGGER data_native_archive_claims_no_update")
	abandonExec(t, tx, "DROP TRIGGER data_native_point_consumers_no_update")
	abandonExec(t, tx, "UPDATE data_native_archive_claims SET canonical=? WHERE archive_id=?", changed, p.ArchiveID)
	abandonExec(t, tx, "UPDATE data_native_point_consumers SET generation=?,canonical=? WHERE point_id=?", p.Generation, changed, point.view.PointID)
	if actual, err := readNativePointConsumer(ctx, tx, point.view.PointID); err != nil || !bytes.Equal(actual, changed) {
		t.Fatal("wrong generation fixture is not internally canonical", err)
	}
	_, err = checkNativeArchivedClaimTx(ctx, tx, p, o, d)
	var integrity *IntegrityError
	if !errors.As(err, &integrity) {
		t.Fatal("host generation replaced the original source generation", err)
	}
}

func TestNativeArchiveConsumerRejectsNoncanonicalPointConsumer(t *testing.T) {
	tx, p, o, d := testNativeArchiveClaim(t, false)
	ctx := t.Context()
	raw, err := readNativePointConsumer(ctx, tx, d.Dependencies[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	changed := append(bytes.Clone(raw), '\n')
	abandonExec(t, tx, "DROP TRIGGER data_native_point_consumers_no_update")
	abandonExec(t, tx, "UPDATE data_native_point_consumers SET canonical=? WHERE point_id=?", changed, d.Dependencies[0].ID)
	_, err = checkNativeArchivedClaimTx(ctx, tx, p, o, d)
	var integrity *IntegrityError
	if !errors.As(err, &integrity) {
		t.Fatal("noncanonical point consumer admitted", err)
	}
}
