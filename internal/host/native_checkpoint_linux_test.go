//go:build linux

package host

import (
	"context"
	"testing"

	"github.com/ShaulLavo/brine/internal/spacetimedb"
	"github.com/ShaulLavo/brine/internal/store"
)

func TestNativeCheckpointRequiresActualStoppedAndOwnedObservation(t *testing.T) {
	ctx := context.Background()
	if _, err := observeOwnedNative(ctx, nil, store.EnginePermit{}, engineMaterial{}); err == nil {
		t.Fatal("caller metadata expectation replaced native process authority")
	}
	if nativeObservationMatches(store.EnginePermit{}.Runtime, spacetimedb.Observation{}) {
		t.Fatal("empty metadata observation accepted")
	}
	if _, err := buildNativeCheckpoint(ctx, &store.Store{}, t.TempDir(), &store.NativeStoppedPreparation{}, nil, nativeOwnedObservation{}, 1); err == nil {
		t.Fatal("structural source/checkpoint manufactured stopped authority")
	}
	if (engineSealedCheckpoint{}).unchanged(ctx) == nil {
		t.Fatal("uninitialized checkpoint proved source stability")
	}
}

func TestNativeCheckpointMemberSelectionSeparatesControlFromHistory(t *testing.T) {
	for _, name := range []string{"config.toml", "metadata.toml", "control-db/conf", "program-bytes/module.wasm"} {
		if !nativeCheckpointMember(name) {
			t.Fatal("controlled metadata member omitted")
		}
	}
	for _, name := range []string{"replicas/1/db.lock", "replicas/1/clog/00000000000000000000.stdb.log", "credentials/password", "publisher.jwt", "program-bytes-other/module.wasm"} {
		if nativeCheckpointMember(name) {
			t.Fatal("history or private runtime material entered metadata selection")
		}
	}
}
