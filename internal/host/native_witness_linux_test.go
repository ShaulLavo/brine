//go:build linux

package host

import (
	"context"
	"testing"

	"github.com/ShaulLavo/brine/internal/localexec"
)

func TestNativeStoppedWitnessRefusesDecodedOrUnownedEvidence(t *testing.T) {
	if _, err := readNativeStoppedWitness(context.Background(), localexec.Session{}, nativeTestPin(), nil, engineSealedCheckpoint{}); err == nil {
		t.Fatal("missing source/view authority produced a local witness")
	}
}
