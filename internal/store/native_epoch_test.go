//go:build linux

package store

import (
	"context"
	"strings"
	"testing"

	"github.com/ShaulLavo/brine/internal/data"
)

func dropNativeTransitionMigrationFixture(t *testing.T, ctx context.Context, s *Store) {
	t.Helper()
	dropNativeCaptureMigrationFixture(t, ctx, s)
	for _, table := range []string{"data_native_epoch_materials", "data_engine_transition_events", "data_native_epochs", "data_engine_transition_inputs"} {
		if _, err := s.db.ExecContext(ctx, "DROP TABLE "+table); err != nil {
			t.Fatal(err)
		}
	}
}

func TestNativeMigrationCreatesImmutableRecordsWithoutChangingSQLite(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	reserved, err := s.ReserveDatabase(ctx, dataRequest())
	if err != nil {
		t.Fatal(err)
	}
	var before string
	if err = s.db.QueryRowContext(ctx, "SELECT hex(canonical) FROM data_databases WHERE id=?", reserved.Database.DatabaseID).Scan(&before); err != nil {
		t.Fatal(err)
	}
	dropNativeTransitionMigrationFixture(t, ctx, s)
	if _, err = s.db.ExecContext(ctx, "UPDATE schema_version SET version=15"); err != nil {
		t.Fatal(err)
	}
	if err = s.migrate(ctx); err != nil {
		t.Fatal(err)
	}
	var after string
	if err = s.db.QueryRowContext(ctx, "SELECT hex(canonical) FROM data_databases WHERE id=?", reserved.Database.DatabaseID).Scan(&after); err != nil || before != after {
		t.Fatal("migration changed historical SQLite bytes", err)
	}
	if _, err = s.ReadReplicaPermit(ctx, reserved.Database.DatabaseID); err != nil {
		t.Fatal("migration changed SQLite permit", err)
	}
}

func TestNativePrefixConflictIncludesRetainedSameBinding(t *testing.T) {
	f := newNativeTransitionFixture(t)
	claimTransitionFixture(t, f)
	ctx := context.Background()
	_, archive, err := readNativeEpoch(ctx, f.store.db, f.runtime.BindingID, f.runtime.EpochID)
	if err != nil {
		t.Fatal(err)
	}
	if err = checkFreshNativePrefix(ctx, f.store.db, archive); err == nil {
		t.Fatal("same binding reused a retained epoch prefix")
	}
	foreign := data.ReplicaBindingID(strings.Repeat("1", 32))
	if err = checkNativeDestinationOwner(ctx, f.store.db, archive.Destination.Endpoint, archive.Destination.Bucket, archive.RemotePrefix+"/nested", foreign); err == nil {
		t.Fatal("other binding acquired overlapping retained prefix")
	}
	if err = checkNativeDestinationOwner(ctx, f.store.db, archive.Destination.Endpoint, archive.Destination.Bucket, archive.RemotePrefix, archive.BindingID); err != nil {
		t.Fatal("active owner cannot read its own immutable epoch", err)
	}
}
