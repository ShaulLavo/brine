//go:build linux

package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ShaulLavo/brine/internal/data"
)

func TestRestorePointDurabilityAndExactEvidence(t *testing.T) {
	ctx := context.Background()
	state, binding := committedDataFixture(t)
	now := time.Now().UTC()
	point := data.RestorePoint{ID: strings.Repeat("d", 32), BindingID: binding.BindingID, EpochID: binding.EpochID, Kind: data.RestorePointLTX, Schema: data.SchemaObservation{DatabaseID: binding.DatabaseID, State: data.VerifiedEmpty, Marker: data.EmptyMarker, CatalogSHA256: data.EmptyCatalogSHA256, ObservedAt: now}, RecordedAt: now, LTX: &data.RestoreLTXPoint{TXID: 7}}
	if err := state.SaveRestorePoint(ctx, point); !errors.Is(err, ErrInvalid) {
		t.Fatal("barrier-less recoverability proof accepted as exact point", err)
	}
	point.LTX.Barrier = data.RestoreUploadBarrier{BindingID: binding.BindingID, EpochID: binding.EpochID, TXID: 7, ReplicaTXID: 7, ObservedAt: now}
	if err := state.SaveRestorePoint(ctx, point); err != nil {
		t.Fatal(err)
	}
	if err := state.SaveRestorePoint(ctx, point); err != nil {
		t.Fatal("exact retry failed", err)
	}
	readonly, err := OpenReadOnly(ctx, state.dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = readonly.Close() }()
	got, err := readonly.ReadRestorePoint(ctx, point.ID, point.BindingID, point.EpochID)
	if err != nil || got.LTX == nil || got.LTX.TXID != 7 {
		t.Fatal("durable point lost", err)
	}
	if _, err := readonly.ReadRestorePoint(ctx, point.ID, point.BindingID, data.ReplicaEpochID(strings.Repeat("e", 32))); !errors.Is(err, ErrNotFound) {
		t.Fatal("foreign epoch accepted", err)
	}
	changed := point
	changed.RecordedAt = now.Add(time.Second)
	if err := state.SaveRestorePoint(ctx, changed); !errors.Is(err, ErrConflict) {
		t.Fatal("point identity overwritten", err)
	}
	if err := readonly.SaveRestorePoint(ctx, point); !errors.Is(err, ErrInvalid) {
		t.Fatal("read-only store wrote point", err)
	}
}

func TestEmptyPointNeedsMatchingHeldInitializationFence(t *testing.T) {
	ctx := context.Background()
	state := openTest(t)
	reserved, err := state.ReserveDatabase(ctx, dataRequest())
	if err != nil {
		t.Fatal(err)
	}
	fence, err := state.HoldDataFence(ctx, reserved.Database.DatabaseID, "init-operation")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	point := data.RestorePoint{ID: strings.Repeat("a", 32), BindingID: reserved.Replica.BindingID, EpochID: reserved.Replica.EpochID, Kind: data.RestorePointSnapshot, Schema: data.SchemaObservation{DatabaseID: reserved.Database.DatabaseID, State: data.VerifiedEmpty, Marker: data.EmptyMarker, CatalogSHA256: data.EmptyCatalogSHA256, ObservedAt: now}, RecordedAt: now, Snapshot: &data.RestoreSnapshotPoint{ObjectKey: reserved.Replica.RemotePrefix + "restore-points/" + strings.Repeat("a", 32) + "/snapshot.sqlite", SHA256: strings.Repeat("b", 64), Size: 8192}}
	if err := state.SaveRestorePoint(ctx, point); !errors.Is(err, ErrConflict) {
		t.Fatal("ordinary save admitted uncommitted binding", err)
	}
	if err := state.SaveEmptyRestorePoint(ctx, point, "other-operation", fence.ID); !errors.Is(err, ErrConflict) {
		t.Fatal("foreign operation fence accepted", err)
	}
	if err := state.SaveEmptyRestorePoint(ctx, point, "init-operation", fence.ID); err != nil {
		t.Fatal(err)
	}
	if err := state.SaveEmptyRestorePoint(ctx, point, "init-operation", fence.ID); err != nil {
		t.Fatal("same empty point retry failed", err)
	}
	if err := state.ReleaseDataFence(ctx, fence.ID, "init-operation"); err != nil {
		t.Fatal(err)
	}
	if err := state.SaveEmptyRestorePoint(ctx, point, "init-operation", fence.ID); !errors.Is(err, ErrConflict) {
		t.Fatal("released fence accepted", err)
	}
}

func committedDataFixture(t *testing.T) (*Store, data.ReplicaBinding) {
	t.Helper()
	ctx := context.Background()
	state := openTest(t)
	reserved, err := state.ReserveDatabase(ctx, dataRequest())
	if err != nil {
		t.Fatal(err)
	}
	binding := reserved.Replica
	binding.ConfigContent = "dbs: []\n"
	sum := sha256.Sum256([]byte(binding.ConfigContent))
	binding.ConfigSHA256 = hex.EncodeToString(sum[:])
	binding.ConfigFile = filepath.Join(state.dir, "replication", string(binding.BindingID), "configs", binding.ConfigSHA256+".yml")
	binding.SocketFile = filepath.Join(state.dir, "replication", string(binding.BindingID), "control.sock")
	binding.LifetimeLockFile = filepath.Join(state.dir, "replica-locks", string(binding.BindingID)+".lock")
	binding.UnitSHA256 = strings.Repeat("c", 64)
	binding.CredentialVersion = 1
	binding.CredentialFile = filepath.Join(state.dir, "credentials/s3/primary/v1.env")
	if err := state.CommitReplicaBinding(ctx, binding); err != nil {
		t.Fatal(err)
	}

	permit, err := state.ReadReplicaPermitByBinding(ctx, binding.BindingID)
	if err != nil {
		t.Fatal(err)
	}
	return state, permit.Replica
}
