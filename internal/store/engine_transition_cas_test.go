//go:build linux

package store

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ShaulLavo/brine/internal/data"
)

func TestNativeTransitionCASHasOneWinnerAndBlocksGenericFenceRelease(t *testing.T) {
	f := newNativeTransitionFixture(t)
	old := claimTransitionFixture(t, f)
	observed := storeTransitionLayout(t, f.input, data.EngineLayoutPrepared)
	results := make(chan error, 2)
	var workers sync.WaitGroup
	for range 2 {
		workers.Add(1)
		go func() {
			defer workers.Done()
			_, err := f.store.AdvanceEngineTransition(context.Background(), f.preparation, old, data.EngineTransitionPreserveIntent, observed)
			results <- err
		}()
	}
	workers.Wait()
	close(results)
	winners := 0
	for err := range results {
		if err == nil {
			winners++
		}
	}
	if winners != 1 {
		t.Fatal("cursor CAS did not elect one winner", winners)
	}
	if err := f.preparation.Close(); err != nil {
		t.Fatal(err)
	}
	c := f.input.Common()
	if err := f.store.ReleaseDataFence(context.Background(), c.FenceID, c.FenceOwnerOperationID); err == nil {
		t.Fatal("generic release bypassed nonterminal data transition")
	}
	if _, err := f.store.AdvanceEngineTransition(context.Background(), f.preparation, old, data.EngineTransitionPreserveIntent, observed); err == nil {
		t.Fatal("closed guard retained mutation authority")
	}
}

func TestNativeActivationFailureRollsBackRuntimeMaterialAndCursor(t *testing.T) {
	f := newNativeTransitionFixture(t)
	old := activationCursorFixture(t, f)
	ctx := context.Background()
	before, _, err := f.store.ReadEngineHead(ctx, f.runtime.BindingID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.store.db.ExecContext(ctx, "CREATE TRIGGER fixture_reject_activation BEFORE INSERT ON data_engine_transition_events WHEN NEW.stage='activated' BEGIN SELECT RAISE(ABORT,'injected activation failure'); END;"); err != nil {
		t.Fatal(err)
	}
	tx, cancel, err := f.store.beginWrite(ctx)
	defer cancel()
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.store.activateEngineTransitionTx(ctx, tx, f.preparation, old, f.runtime, f.allocation); err == nil {
		t.Fatal("injected transaction failure ignored")
	}
	if err = tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	after, _, err := f.store.ReadEngineHead(ctx, f.runtime.BindingID)
	if err != nil || after != before {
		t.Fatal("failed activation changed active runtime", err)
	}
	_, cursor, err := f.store.ReadEngineTransition(ctx, old.ID)
	if err != nil || cursor != old {
		t.Fatal("failed activation advanced cursor", err)
	}
	var materials int
	if err = f.store.db.QueryRowContext(ctx, "SELECT count(*) FROM data_native_epoch_materials").Scan(&materials); err != nil || materials != 0 {
		t.Fatal("failed activation left incoming allocation", err)
	}
}

func TestNativeActivationRejectsWrongAllocationRuntimeAndPreparedConsumer(t *testing.T) {
	f := newNativeTransitionFixture(t)
	old := activationCursorFixture(t, f)
	ctx := context.Background()
	for _, changed := range []data.EngineRuntime{
		func() data.EngineRuntime { r := f.runtime.Clone(); r.Secrets.SigningPublic.Version++; return r }(),
		func() data.EngineRuntime {
			r := f.runtime.Clone()
			r.EpochID = data.ReplicaEpochID(strings.Repeat("d", 32))
			return r
		}(),
	} {
		tx, cancel, err := f.store.beginWrite(ctx)
		if err != nil {
			cancel()
			t.Fatal(err)
		}
		_, err = f.store.activateEngineTransitionTx(ctx, tx, f.preparation, old, changed, f.allocation)
		_ = tx.Rollback()
		cancel()
		if err == nil {
			t.Fatal("changed runtime/key epoch accepted")
		}
	}
	wrong := cloneNativeAllocation(f.allocation)
	wrong.DirectoryInode++
	tx, cancel, err := f.store.beginWrite(ctx)
	defer cancel()
	if err != nil {
		t.Fatal(err)
	}
	_, err = f.store.activateEngineTransitionTx(ctx, tx, f.preparation, old, f.runtime, wrong)
	_ = tx.Rollback()
	if err == nil {
		t.Fatal("foreign incoming inode accepted")
	}
	if err := f.preparation.Close(); err != nil {
		t.Fatal(err)
	}
	ctx, stop := context.WithTimeout(ctx, time.Second)
	defer stop()
	if err := f.store.ReleaseDataFence(ctx, f.input.Common().FenceID, "unrelated-owner"); err == nil {
		t.Fatal("foreign operation released preparation fence")
	}
}
