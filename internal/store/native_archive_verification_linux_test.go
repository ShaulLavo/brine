//go:build linux

package store

import (
	"context"
	"encoding/json"
	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/restic"
	"testing"
)

func TestNativeArchiveVerificationAndMovementRefuseDecodedProofs(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	if _, err := json.Marshal(NativeArchiveVerificationObservation{}); err == nil {
		t.Fatal("archive proof became RPC input")
	}
	if _, err := json.Marshal(NativeArchiveTreeObservation{}); err == nil {
		t.Fatal("archive movement became RPC input")
	}
	if _, err := s.RecordNativeArchiveVerified(ctx, data.ArchiveID("0123456789abcdef0123456789abcdef"), nil, restic.AuthenticatedRestore{}, NativeArchiveVerificationObservation{}); err == nil {
		t.Fatal("missing17stop minted21coverage")
	}
	if _, _, err := s.BeginNativeArchiveTreeMove(ctx, data.ArchiveID("0123456789abcdef0123456789abcdef"), "abcdef0123456789abcdef0123456789", nil, nil); err == nil {
		t.Fatal("missing journal/locks admitted tree movement")
	}
	if err := s.CompleteNativeArchiveTreeMove(ctx, NativeArchiveTreeMove{}, NativeArchiveTreeObservation{}); err == nil {
		t.Fatal("decoded move receipt admitted retirement")
	}
}
