package store

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/ShaulLavo/brine/internal/data"
)

func engineReservation(t *testing.T) EngineReservation {
	t.Helper()
	sqlite := dataRequest()
	database := data.SpacetimeDBBinding{DatabaseID: data.DatabaseID(strings.Repeat("a", 32)), Name: "game", IncarnationID: data.AppIncarnationID(strings.Repeat("b", 32)), Root: sqlite.Database.PersistentRoot, BindingID: data.ReplicaBindingID(strings.Repeat("c", 32))}
	relative, err := data.RelativeDirectory(database.IncarnationID, database.DatabaseID)
	if err != nil {
		t.Fatal(err)
	}
	database.RelativeDirectory = relative
	epoch := data.ReplicaEpochID(strings.Repeat("d", 32))
	prefix, err := data.RemotePrefix(sqlite.Destination.BasePrefix, database.IncarnationID, database.DatabaseID, epoch)
	if err != nil {
		t.Fatal(err)
	}
	return EngineReservation{App: "managed", PolicyHash: sqlite.PolicyHash, Database: database, Archive: data.ArchiveBinding{BindingID: database.BindingID, DatabaseID: database.DatabaseID, Destination: sqlite.Destination, EpochID: epoch, RemotePrefix: prefix}}
}

func dropEngineMigrationFixture(t *testing.T, ctx context.Context, s *Store) {
	t.Helper()
	for _, table := range []string{"data_engine_runtime_commits", "data_engine_modules", "data_engine_kinds"} {
		if _, err := s.db.ExecContext(ctx, "DROP TABLE "+table); err != nil {
			t.Fatal(err)
		}
	}
}

func TestEngineMigrationPreservesSQLiteBytes(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	reserved, err := s.ReserveDatabase(ctx, dataRequest())
	if err != nil {
		t.Fatal(err)
	}
	var before []byte
	if err = s.db.QueryRowContext(ctx, "SELECT canonical FROM data_databases WHERE id=?", reserved.Database.DatabaseID).Scan(&before); err != nil {
		t.Fatal(err)
	}
	dropEngineMigrationFixture(t, ctx, s)
	if _, err = s.db.ExecContext(ctx, "UPDATE schema_version SET version=14"); err != nil {
		t.Fatal(err)
	}
	if err = s.migrate(ctx); err != nil {
		t.Fatal(err)
	}
	var after []byte
	var kind data.EngineKind
	if err = s.db.QueryRowContext(ctx, "SELECT d.canonical,k.engine FROM data_databases d JOIN data_engine_kinds k ON k.database_id=d.id WHERE d.id=?", reserved.Database.DatabaseID).Scan(&after, &kind); err != nil || !bytes.Equal(before, after) || kind != data.EngineSQLite {
		t.Fatal("migration changed SQLite identity bytes", err)
	}
	if _, err = s.ReadReplicaPermit(ctx, reserved.Database.DatabaseID); err != nil {
		t.Fatal("historical SQLite permit changed", err)
	}
}

func TestEngineReservationIsImmutableAndNeverSQLite(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	request := engineReservation(t)
	first, err := s.ReserveEngine(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	second, err := s.ReserveEngine(ctx, request)
	if err != nil || first != second {
		t.Fatal("reservation was not idempotent", err)
	}
	if _, err = s.ReadReplicaPermit(ctx, first.Database.DatabaseID); err == nil {
		t.Fatal("engine tree consumed as a SQLite binding")
	}
	if _, err = s.db.ExecContext(ctx, "UPDATE data_engine_kinds SET engine='sqlite' WHERE database_id=?", first.Database.DatabaseID); err == nil {
		t.Fatal("engine identity was mutable")
	}
}

func TestManagedFenceWaitsOnlyForLaunchGuard(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	r, err := s.ReserveEngine(ctx, engineReservation(t))
	if err != nil {
		t.Fatal(err)
	}
	if err = data.PrepareEngineLocks(ctx, s.dir, r.Database.BindingID); err != nil {
		t.Fatal(err)
	}
	launch, err := data.AcquireEngineLaunchGuard(ctx, s.dir, r.Database.BindingID)
	if err != nil {
		t.Fatal(err)
	}
	lifetime, err := data.AcquireEngineLifetimeLock(ctx, s.dir, r.Database.BindingID)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = lifetime.Release() }()
	wait, cancel := context.WithTimeout(ctx, 30*time.Millisecond)
	defer cancel()
	if _, err = s.HoldDataFence(wait, r.Database.DatabaseID, "fixture"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("fence did not wait for startup handoff", err)
	}
	if err = launch.Release(); err != nil {
		t.Fatal(err)
	}
	fence, err := s.HoldDataFence(ctx, r.Database.DatabaseID, "fixture")
	if err != nil || fence.State != data.FenceHeld {
		t.Fatal("runtime lifetime lock blocked fence claim", err)
	}
	if err = s.ReleaseDataFence(ctx, fence.ID, "fixture"); err != nil {
		t.Fatal(err)
	}
}
