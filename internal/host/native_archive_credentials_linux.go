//go:build linux

package host

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"reflect"
	"time"

	"github.com/ShaulLavo/brine/internal/archive"
	"github.com/ShaulLavo/brine/internal/backupcredentials"
	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/store"
)

func (h archiveHost) nativeArchiveCredentialHealth(ctx context.Context, p archive.Plan, d archive.NativeDatabase) ([]archive.CredentialHealth, error) {
	lifetime, err := data.AcquireEngineLifetimeLock(ctx, h.stateRoot, d.Binding.BindingID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = lifetime.Release() }()
	preparation, err := h.service.Store.AcquireNativeArchivePreparation(ctx, p.ArchiveID, d.Binding.BindingID, lifetime)
	if err != nil {
		return nil, err
	}
	defer func() { _ = preparation.Close() }()
	capability, err := h.service.Store.NativeArchivedSource(ctx, p.ArchiveID, d.Binding.BindingID, preparation, lifetime)
	if err != nil {
		return nil, err
	}
	reader, err := h.service.Store.NativeArchivedRepository(ctx, capability)
	var selected store.NativeRepositoryMaterial
	if err == nil {
		selected, err = reader.Material(ctx)
	}
	files := backupcredentials.Files{Root: filepath.Join(h.stateRoot, "credentials")}
	out := make([]archive.CredentialHealth, len(d.Epochs))
	for index, epoch := range d.Epochs {
		health := archive.CredentialHealth{BindingID: epoch.BindingID, EpochID: epoch.EpochID}
		receipt, readErr := h.service.Store.ReadArchiveCredentialReceipt(ctx, p.ArchiveID, epoch.BindingID, epoch.EpochID)
		if errors.Is(readErr, sql.ErrNoRows) && err == nil && selected.Scope.EpochID == string(epoch.EpochID) {
			health.ReceivedAt, health.ExpiresAt = selected.CredentialRecord.ReceivedAt, selected.CredentialRecord.ExpiresAt
			packet, packetErr := nativeRepositoryCredentialPacket(files, selected)
			health.Missing = packetErr != nil
			packet.Clear()
		} else if readErr != nil {
			health.Missing = true
		} else {
			health = nativeArchiveRenewedCredentialHealth(files, receipt, health)
		}
		now := time.Now().UTC()
		if !health.ReceivedAt.IsZero() && now.After(health.ReceivedAt) {
			health.AgeSeconds = int64(now.Sub(health.ReceivedAt) / time.Second)
		}
		if health.ExpiresAt != nil {
			health.Expired = !now.Before(*health.ExpiresAt)
		}
		health.RenewalNeeded = health.Missing || health.Expired || health.ExpiresAt != nil && health.ExpiresAt.Sub(now) < 15*time.Minute
		out[index] = health
	}
	return out, nil
}

func nativeArchiveRenewedCredentialHealth(files backupcredentials.Files, receipt backupcredentials.ArchiveReceipt, health archive.CredentialHealth) archive.CredentialHealth {
	sealed, err := files.ArchiveReceipt(receipt.Plan.Scope.FileReference(), receipt.Plan.Version)
	if err != nil || !reflect.DeepEqual(sealed, receipt) {
		health.Missing = true
		return health
	}
	health.ReceivedAt, health.ExpiresAt = receipt.ReceivedAt, receipt.Plan.ExpiresAt
	packet, err := files.Read(receipt.Plan.Scope.FileReference(), receipt.Plan.Version)
	health.Missing = err != nil
	packet.Clear()
	return health
}
