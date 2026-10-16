//go:build linux

package store

import (
	"strings"
	"testing"

	"github.com/ShaulLavo/brine/internal/data"
)

func TestManagedRouteReceiptCanFollowRetiredBinding(t *testing.T) {
	s, p, op := managedControlFixture(t)
	ctx := t.Context()
	current := ManagedDesiredState{BindingID: p.Managed.Database.BindingID, EpochID: p.Managed.Archive.EpochID, RuntimeSHA256: p.Managed.RuntimeSHA256, Revision: 1, PlanID: p.Hash, OperationID: op.ID, EngineRunning: true, IngressServing: true}
	if _, err := s.CommitManagedDesired(ctx, op.ID, 0, current); err != nil {
		t.Fatal(err)
	}
	r := EngineReservation{App: p.App, PolicyHash: p.PolicyHash, Database: p.Managed.Database, Archive: p.Managed.Archive}
	r.Database.IncarnationID, r.Database.DatabaseID, r.Database.BindingID = data.AppIncarnationID(strings.Repeat("1", 32)), data.DatabaseID(strings.Repeat("2", 32)), data.ReplicaBindingID(strings.Repeat("3", 32))
	var err error
	r.Database.RelativeDirectory, err = data.RelativeDirectory(r.Database.IncarnationID, r.Database.DatabaseID)
	if err != nil {
		t.Fatal(err)
	}
	r.Archive.BindingID, r.Archive.DatabaseID, r.Archive.EpochID = r.Database.BindingID, r.Database.DatabaseID, data.ReplicaEpochID(strings.Repeat("4", 32))
	r.Archive.RemotePrefix, err = data.RemotePrefix(r.Archive.Destination.BasePrefix, r.Database.IncarnationID, r.Database.DatabaseID, r.Archive.EpochID)
	if err != nil {
		t.Fatal(err)
	}
	abandonExec(t, s.db, "INSERT INTO data_incarnations VALUES(?,?)", r.Database.IncarnationID, p.App)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err = insertEngineReservation(ctx, tx, r); err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	wanted := ManagedRouteState{App: p.App, BindingID: current.BindingID, EpochID: current.EpochID, RuntimeSHA256: current.RuntimeSHA256, DesiredRevision: current.Revision, PlanID: p.Hash, OperationID: op.ID, Revision: 2, Generation: 2, Present: true, FileHash: "sha256:" + strings.Repeat("a", 64), RootSHA256: strings.Repeat("b", 64), LoadedSHA256: strings.Repeat("c", 64)}
	prior := wanted
	prior.BindingID, prior.EpochID, prior.Revision, prior.Generation, prior.Present, prior.FileHash = r.Database.BindingID, r.Archive.EpochID, 1, 1, false, ""
	abandonExec(t, s.db, "INSERT INTO data_engine_route_states VALUES(?,?,?,?,?,?)", prior.App, prior.BindingID, prior.Revision, prior.PlanID, prior.OperationID, abandonJSON(t, prior))
	abandonExec(t, s.db, "INSERT INTO data_engine_route_heads VALUES(?,?)", prior.App, prior.Revision)
	if _, err := s.CommitManagedRouteState(ctx, 1, wanted); err == nil {
		t.Fatal("binding changed without a retirement receipt")
	}
	archiveID := strings.Repeat("5", 32)
	archivePlan := "sha256:" + strings.Repeat("6", 64)
	abandonExec(t, s.db, "INSERT INTO data_archive_plans VALUES(?,?,?)", archivePlan, archiveID, []byte(`{}`))
	abandonExec(t, s.db, "INSERT INTO data_archive_retirements VALUES(?,?,?,?,?)", archiveID, r.Database.IncarnationID, p.App, "", []byte(`{}`))
	if _, err := s.CommitManagedRouteState(ctx, 1, wanted); err != nil {
		t.Fatal("retired binding blocks the recreated route receipt", err)
	}
	var retained []byte
	if err := s.db.QueryRowContext(ctx, "SELECT canonical FROM data_engine_route_states WHERE app=? AND revision=1", p.App).Scan(&retained); err != nil || string(retained) != string(abandonJSON(t, prior)) {
		t.Fatal("recreation changed archived route history", err)
	}
}
