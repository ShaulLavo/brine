//go:build linux

package store

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/ShaulLavo/brine/internal/data"
)

func initialStageFixture(t *testing.T) (*NativeInitialStage, *Store) {
	t.Helper()
	s, prep, lifetime, _ := nativePreparationFixture(t)
	stopped, err := s.RecordNativeStoppedPreparation(context.Background(), prep, lifetime, nativeAbsentStopFixture())
	if err != nil {
		t.Fatal(err)
	}
	stage, err := s.BeginNativeInitialStage(context.Background(), stopped, strings.Repeat("1", 32), strings.Repeat("2", 32), strings.Repeat("3", 32), strings.Repeat("4", 64))
	if err != nil {
		t.Fatal(err)
	}
	return stage, s
}

func TestNativeInitialStageRequiresScopeAndOrderedImmutableObservations(t *testing.T) {
	stage, s := initialStageFixture(t)
	ctx := context.Background()
	if _, err := s.BeginNativeInitialStage(ctx, &NativeStoppedPreparation{}, strings.Repeat("5", 32), strings.Repeat("6", 32), strings.Repeat("7", 32), strings.Repeat("8", 64)); err == nil {
		t.Fatal("structural stopped source admitted")
	}
	observed := NativeInitialStageObservation{Stage: "copy_ready", Parent: &data.EngineDirectoryIdentity{Device: 1, Inode: 2}, Directory: &data.EngineDirectoryIdentity{Device: 1, Inode: 3}, At: time.Now().UTC()}
	if stage.Record(ctx, observed) == nil {
		t.Fatal("skipped workspace issue")
	}
	observed.Stage, observed.Directory = "workspace_created", nil
	if err := stage.Record(ctx, observed); err != nil {
		t.Fatal(err)
	}
	observed.Stage, observed.Parent.Inode = "copy_ready", 9
	if stage.Record(ctx, observed) == nil {
		t.Fatal("changed owned parent identity")
	}
	observed.Parent.Inode, observed.Directory = 2, &data.EngineDirectoryIdentity{Device: 1, Inode: 3}
	if err := stage.Record(ctx, observed); err != nil {
		t.Fatal(err)
	}
	if stage.Record(ctx, observed) == nil {
		t.Fatal("repeated conflicting phase admitted")
	}
	stage.intent.EpochID = data.ReplicaEpochID(strings.Repeat("9", 32))
	stage.raw = []byte("changed")
	observed.Stage, observed.SourceSHA256 = "source_ready", strings.Repeat("a", 64)
	if !errors.Is(stage.Record(ctx, observed), ErrConflict) {
		t.Fatal("forged retained source admitted")
	}
}

func TestNativeInitialStageReadRejectsCoherentForeignScope(t *testing.T) {
	stage, s := initialStageFixture(t)
	ctx := context.Background()
	if _, _, err := readNativeInitialStage(ctx, s.db, stage.intent.Nonce); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.ExecContext(ctx, "UPDATE data_native_initial_stage_intents SET operation_id='foreign' WHERE nonce=?", stage.intent.Nonce); err == nil {
		t.Fatal("intent identity mutable")
	}
	if _, err := s.db.ExecContext(ctx, "DELETE FROM data_native_initial_stage_intents WHERE nonce=?", stage.intent.Nonce); err == nil {
		t.Fatal("intent not retained")
	}
	if _, err := s.db.ExecContext(ctx, "DROP TRIGGER data_native_initial_stage_intents_no_update"); err != nil {
		t.Fatal(err)
	}
	forged := stage.intent
	forged.EpochID = data.ReplicaEpochID(strings.Repeat("a", 32))
	raw, _ := json.Marshal(forged)
	if _, err := s.db.ExecContext(ctx, "UPDATE data_native_initial_stage_intents SET canonical=? WHERE nonce=?", raw, forged.Nonce); err != nil {
		t.Fatal(err)
	}
	if _, _, err := readNativeInitialStage(ctx, s.db, forged.Nonce); err == nil {
		t.Fatal("coherently encoded foreign epoch admitted")
	}
}
