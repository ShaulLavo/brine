//go:build linux

package store

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestNativePointRejectsCrossedDependenciesWithConsistentEventChain(t *testing.T) {
	for _, field := range []string{"source_sha256", "source_witness_sha256", "checkpoint_sha256", "transport_point_sha256", "bundle_manifest_sha256"} {
		t.Run(field, func(t *testing.T) { checkCrossedNativePointDependency(t, field) })
	}
}

func checkCrossedNativePointDependency(t *testing.T, field string) {
	t.Helper()
	state, preparation, point := nativeCompletedPointFixture(t)
	ctx := context.Background()
	events, err := readNativePreparationEvents(ctx, state.db, preparation.intent, preparation.raw)
	if err != nil {
		t.Fatal(err)
	}
	index := 2
	if field == "transport_point_sha256" || field == "bundle_manifest_sha256" {
		index = 3
	}
	var facts map[string]json.RawMessage
	if err := json.Unmarshal(events[index].Proof, &facts); err != nil {
		t.Fatal(err)
	}
	facts[field], err = json.Marshal(strings.Repeat("f", 64))
	if err != nil {
		t.Fatal(err)
	}
	events[index].Proof, err = json.Marshal(facts)
	if err != nil {
		t.Fatal(err)
	}
	rewriteNativePreparationChainFixture(t, state, preparation, events)
	if _, err := state.ReadNativePreparedPoint(ctx, point.PointID); err == nil {
		t.Fatal("crossed dependency survived a coherent event chain")
	}
}

func rewriteNativePreparationChainFixture(t *testing.T, state *Store, preparation *NativePreparation, events []nativePreparationEvent) {
	t.Helper()
	ctx := context.Background()
	if _, err := state.db.ExecContext(ctx, "DROP TRIGGER data_native_preparation_events_no_update"); err != nil {
		t.Fatal(err)
	}
	previous := nativePointHash(preparation.raw)
	for _, event := range events {
		event.PreviousSHA256 = previous
		raw, err := json.Marshal(event)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := state.db.ExecContext(ctx, "UPDATE data_native_preparation_events SET previous_sha256=?,canonical=? WHERE point_id=? AND sequence=?", previous, raw, preparation.PointID(), event.Sequence); err != nil {
			t.Fatal(err)
		}
		previous = nativePointHash(raw)
	}
}
