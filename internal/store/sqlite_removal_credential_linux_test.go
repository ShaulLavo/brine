//go:build linux

package store

import (
	"testing"

	"github.com/ShaulLavo/brine/internal/data"
)

func TestSQLiteRemovalCredentialRetainsExactDeliveryAfterRecreation(t *testing.T) {
	r := newRemovalFixture(t, false)
	ref := r.plan.Removal.Points[0]
	before, err := r.fx.s.ReadSQLiteRemovalCredential(t.Context(), r.plan.ID, ref.DatabaseID)
	if err != nil || before.Version != ref.CredentialVersion {
		t.Fatal("unclaimed protected delivery unavailable", err)
	}
	o := r.preparedArchive(r.claim())
	r.retire(o)
	req := dataRequest()
	req.App, req.PolicyHash = r.fx.app, r.policy.Hash()
	newState, err := r.fx.s.ReserveDatabase(t.Context(), req)
	if err != nil || newState.Database.IncarnationID == r.plan.Candidate.Incarnation {
		t.Fatal("new incarnation not reserved", err)
	}
	after, err := r.fx.s.ReadSQLiteRemovalCredential(t.Context(), r.plan.ID, ref.DatabaseID)
	if err != nil || after.ID != before.ID || after.Version != ref.CredentialVersion {
		t.Fatal("retired delivery replaced by recreated app", err)
	}
}

func TestSQLiteRemovalCredentialRejectsCrossedRecord(t *testing.T) {
	for _, field := range []string{"app", "version", "id"} {
		t.Run(field, func(t *testing.T) { crossedRemovalCredential(t, field) })
	}
}

func crossedRemovalCredential(t *testing.T, field string) {
	t.Helper()
	r := newRemovalFixture(t, false)
	r.claim()
	ref := r.plan.Removal.Points[0]
	record, err := r.fx.s.ReadSQLiteRemovalCredential(t.Context(), r.plan.ID, ref.DatabaseID)
	if err != nil {
		t.Fatal(err)
	}
	execRemovalFixture(t, r.fx.s, "DROP TRIGGER data_credential_records_no_update")
	query := map[string]string{
		"app":     "UPDATE data_credential_records SET canonical=json_set(canonical,'$.app','other') WHERE id=?",
		"version": "UPDATE data_credential_records SET canonical=json_set(canonical,'$.version',2) WHERE id=?",
		"id":      "UPDATE data_credential_records SET canonical=json_set(canonical,'$.id','ffffffffffffffffffffffffffffffff') WHERE id=?",
	}[field]
	if _, err := r.fx.s.db.ExecContext(t.Context(), query, record.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := r.fx.s.ReadSQLiteRemovalCredential(t.Context(), r.plan.ID, ref.DatabaseID); err == nil {
		t.Fatal("crossed delivery accepted", field)
	}
}

func TestSQLiteRemovalCredentialRejectsUnknownDatabase(t *testing.T) {
	r := newRemovalFixture(t, false)
	if _, err := r.fx.s.ReadSQLiteRemovalCredential(t.Context(), r.plan.ID, data.DatabaseID("ffffffffffffffffffffffffffffffff")); err == nil {
		t.Fatal("foreign database selected a delivery")
	}
}
