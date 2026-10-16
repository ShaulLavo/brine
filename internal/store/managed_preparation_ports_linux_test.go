//go:build linux

package store

import (
	"strings"
	"testing"

	"github.com/ShaulLavo/brine/internal/data"
)

func TestManagedPreparationReusesRetiredPorts(t *testing.T) {
	s, planned, op := managedControlFrontendFixture(t, true)
	ctx := t.Context()
	if planned.Managed.EnginePort > 65535 || planned.Managed.Frontend.Port > 65535 {
		t.Fatal("fixture ports exceed native preparation range")
		return
	}
	old := ManagedPreparation{BindingID: planned.Managed.Database.BindingID, PlanID: planned.Hash, OperationID: op.ID, EnginePort: uint16(planned.Managed.EnginePort), FrontendPort: uint16(planned.Managed.Frontend.Port), ConfigSHA256: strings.Repeat("7", 64)}
	abandonExec(t, s.db, "INSERT INTO data_engine_preparations VALUES(?,?,?,?,?,?)", old.BindingID, old.PlanID, old.OperationID, old.EnginePort, old.FrontendPort, abandonJSON(t, old))
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := admitManagedPorts(ctx, tx, old); err == nil {
		t.Fatal("active preparation lost its port reservation")
	}
	_ = tx.Rollback()
	abandonExec(t, s.db, "DELETE FROM data_active_incarnations WHERE incarnation_id=?", planned.Managed.Database.IncarnationID)
	r := engineReservation(t)
	r.App, r.PolicyHash = planned.App, planned.PolicyHash
	r.Database = planned.Managed.Database
	r.Database.IncarnationID, r.Database.DatabaseID, r.Database.BindingID = data.AppIncarnationID(strings.Repeat("2", 32)), data.DatabaseID(strings.Repeat("3", 32)), data.ReplicaBindingID(strings.Repeat("4", 32))
	r.Database.RelativeDirectory, err = data.RelativeDirectory(r.Database.IncarnationID, r.Database.DatabaseID)
	if err != nil {
		t.Fatal(err)
	}
	r.Archive = planned.Managed.Archive
	r.Archive.BindingID, r.Archive.DatabaseID, r.Archive.EpochID = r.Database.BindingID, r.Database.DatabaseID, data.ReplicaEpochID(strings.Repeat("5", 32))
	r.Archive.RemotePrefix, err = data.RemotePrefix(r.Archive.Destination.BasePrefix, r.Database.IncarnationID, r.Database.DatabaseID, r.Archive.EpochID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.ReserveEngine(ctx, r); err != nil {
		t.Fatal(err)
	}
	fresh := old
	fresh.BindingID = r.Database.BindingID
	tx, err = s.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	if err := admitManagedPorts(ctx, tx, fresh); err != nil {
		t.Fatal("retired preparation still owns ports", err)
	}
	if _, err := tx.ExecContext(ctx, "INSERT INTO data_engine_preparations VALUES(?,?,?,?,?,?)", fresh.BindingID, fresh.PlanID, fresh.OperationID, fresh.EnginePort, fresh.FrontendPort, abandonJSON(t, fresh)); err != nil {
		t.Fatal("historical preparation prevents a fresh port reservation", err)
	}
}
