//go:build linux

package store

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/ShaulLavo/brine/internal/archive"
	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/restic"
)

func TestNativeArchivedSourceRefusesDecodedOrUnownedReadAuthority(t *testing.T) {
	s := openTest(t)
	if _, err := json.Marshal(&NativeArchivedSource{}); err == nil {
		t.Fatal("archive source became serialized read authority")
	}
	if _, err := s.NativeArchivedSource(context.Background(), data.ArchiveID("0123456789abcdef0123456789abcdef"), data.ReplicaBindingID("abcdef0123456789abcdef0123456789"), nil, nil); err == nil {
		t.Fatal("missing process locks admitted archive repository read")
	}
	if _, err := checkNativeArchivedSourceTx(context.Background(), nil, nil); err == nil {
		t.Fatal("missing archive identity admitted read")
	}
}

func TestNativeArchiveReplayCannotUseDecodedReceiptOrMissingExtraction(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	if _, err := s.RecordNativeArchivedReplay(ctx, nil, restic.AuthenticatedRestore{}, NativeArchiveVerificationObservation{}); err == nil {
		t.Fatal("decoded proof created an authenticated replay")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	proof := archive.RetentionProof{NativeReceipts: []archive.NativeRetentionReceipt{{ID: "0123456789abcdef0123456789abcdef"}}}
	if err = checkNativeRetentionReceipts(ctx, tx, proof); err == nil {
		t.Fatal("missing protected replay row admitted retention")
	}
}
