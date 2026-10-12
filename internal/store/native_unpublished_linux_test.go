//go:build linux

package store

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/ShaulLavo/brine/internal/data"
)

func TestNativeEmptyHistoryRefusesPriorPublicationEvenWithUnpublishedHead(t *testing.T) {
	state, preparation, lifetime, _ := nativePreparationFixture(t)
	ctx := context.Background()
	stopped, err := state.RecordNativeStoppedPreparation(ctx, preparation, lifetime, nativeAbsentStopFixture())
	if err != nil {
		t.Fatal(err)
	}
	permit, err := state.ReadNativeStoppedPermit(ctx, stopped)
	if err != nil || checkNativeUntouchedHistory(ctx, state.db, permit.Runtime) != nil {
		t.Fatal("actual untouched history refused", err)
	}
	prior := permit.Runtime.Clone()
	prior.State = data.EngineInitialized
	prior.Database, prior.Publisher, prior.Names = data.NativeDatabaseIdentity(strings.Repeat("1", 64)), data.NativePublisherIdentity(strings.Repeat("2", 64)), []string{"fixture"}
	raw, err := json.Marshal(prior)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := state.db.ExecContext(ctx, "INSERT INTO data_engine_runtime_commits VALUES(?,2,?)", prior.BindingID, raw); err != nil {
		t.Fatal(err)
	}
	if err := checkNativeUntouchedHistory(ctx, state.db, permit.Runtime); err == nil {
		t.Fatal("prior initialized history earned affirmative unpublished provenance")
	}
}
