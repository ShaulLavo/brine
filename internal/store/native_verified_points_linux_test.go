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

func nativeVerifiedPointFixture(preparation *NativePreparation) NativeVerifiedPointView {
	i, now := preparation.intent, time.Now().UTC()
	hash := strings.Repeat("a", 64)
	return NativeVerifiedPointView{Format: "brine-native-verified-point-v1", PointID: i.PointID, OperationID: i.OperationID, BindingID: i.BindingID, EpochID: i.EpochID, DatabaseID: i.DatabaseID, IncarnationID: i.IncarnationID, GenerationID: strings.Repeat("b", 32), RepositoryID: hash, SourceSHA256: hash, CheckpointSHA256: hash, ManifestSHA256: hash, TransportSHA256: hash, SourceEvidenceSHA256: hash, RestoreEvidenceSHA256: hash, ConfirmedBoundSHA256: hash, FinalPairsSHA256: hash, PolicySHA256: i.PolicySHA256, DestinationSHA256: hash, CredentialReference: "backup", CredentialVersion: 1, RecoveryReference: "recovery", RecoveryVersion: 1, CapturedAt: now, VerifiedAt: now, RetainUntil: now.Add(30 * 24 * time.Hour), LaterLoss: "unknown"}
}

func TestNativeVerifiedPointCodecNeverClaimsLaterLossOrExactPreparation(t *testing.T) {
	_, preparation, _, _ := nativePreparationFixture(t)
	v := nativeVerifiedPointFixture(preparation)
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := decodeNativeVerifiedPoint(raw); err != nil {
		t.Fatal("bounded retained reference syntax refused", err)
	}
	for _, invalid := range []string{
		strings.Replace(string(raw), `"format":`, `"fence_owner":"forged","format":`, 1),
		strings.Replace(string(raw), `"committed_offset":0`, `"committed_offset":null`, 1),
		strings.Replace(string(raw), `"later_loss":"unknown"`, `"later_loss":"zero"`, 1),
		strings.Replace(string(raw), `"later_loss":"unknown"`, `"later_loss":"exact"`, 1),
	} {
		if _, err := decodeNativeVerifiedPoint([]byte(invalid)); err == nil {
			t.Fatal("historical point syntax asserted unproven coverage")
		}
	}
}

func TestNativeVerifiedPointReadRequiresProtectedBundleAndCompletedTask(t *testing.T) {
	s, preparation, _, _ := nativePreparationFixture(t)
	ctx, v := context.Background(), nativeVerifiedPointFixture(preparation)
	raw, _ := json.Marshal(v)
	if _, err := s.db.ExecContext(ctx, "INSERT INTO data_native_verified_points VALUES(?,?,?,?,?)", v.PointID, v.OperationID, v.BindingID, v.EpochID, raw); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ReadNativeVerifiedPoint(ctx, v.PointID); err == nil {
		t.Fatal("syntax-only retained point earned restore admission")
	}
	if _, err := s.ReadNativeVerifiedPoint(ctx, strings.Repeat("f", 32)); !errors.Is(err, ErrNotFound) {
		t.Fatal("missing historical point admitted", err)
	}
	if _, err := s.ReadNativePreparedPoint(ctx, v.PointID); !errors.Is(err, ErrNotFound) {
		t.Fatal("ordinary historical point supplied exact preparation", err)
	}
	if _, err := s.db.ExecContext(ctx, "UPDATE data_native_verified_points SET epoch_id=? WHERE id=?", data.ReplicaEpochID(strings.Repeat("f", 32)), v.PointID); err == nil {
		t.Fatal("retained historical point identity mutable")
	}
}
