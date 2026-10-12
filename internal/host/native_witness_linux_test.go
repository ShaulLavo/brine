//go:build linux

package host

import (
	"context"
	"testing"

	"github.com/ShaulLavo/brine/internal/localexec"
	"github.com/ShaulLavo/brine/internal/nativeevidence"
)

func TestNativeStoppedWitnessRefusesDecodedOrUnownedEvidence(t *testing.T) {
	if _, err := readNativeStoppedWitness(context.Background(), localexec.Session{}, nativeTestPin(), nil, engineSealedCheckpoint{}); err == nil {
		t.Fatal("missing source/view authority produced a local witness")
	}
	witness := nativeLocalWitness{evidence: nativeevidence.Evidence{}}
	if witness.matches(engineSealedCheckpoint{}) {
		t.Fatal("decoded evidence replaced owned reader/disposal provenance")
	}
}
