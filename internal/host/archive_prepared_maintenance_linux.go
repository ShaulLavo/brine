//go:build linux

package host

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"reflect"
	"slices"
	"time"

	"github.com/ShaulLavo/brine/internal/archive"
	"github.com/ShaulLavo/brine/internal/backupcredentials"
	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/policy"
	"github.com/ShaulLavo/brine/internal/restore"
)

func (h archiveHost) sqliteArchiveMaintenanceAdmission(ctx context.Context, p archive.Plan, m archive.Manifest, variant archive.Database, pol policy.Policy) (bool, error) {
	d, ok := variant.SQLite()
	if !ok || d.Allocation == nil || !slices.Contains(pol.PersistentRoots(), string(d.Binding.Root)) {
		return false, archive.ErrRefused
	}
	root, err := data.InspectRoot(string(d.Binding.Root))
	if err == nil {
		root, err = root.WithPreparationProof(d.Allocation.RootProof)
	}
	if err != nil || !root.Admits(d.Binding.Root, 0) {
		return false, archive.ErrRefused
	}
	if err = h.archiveMaintenanceFence(ctx, p, m.OperationID, d); err != nil {
		return false, err
	}
	retained := true
	for _, binding := range d.Epochs {
		destination, ok := pol.BackupDestination(binding.Destination.Reference)
		if !ok || destination != binding.Destination {
			return false, archive.ErrRefused
		}
		evidence, known := pol.BackupRetention(binding.Destination.Reference)
		if d.Written && (!known || !evidence.Admits(binding.Destination, time.Now().UTC())) {
			retained = false
		}
	}
	return retained, nil
}

func (h archiveHost) archiveMaintenanceFence(ctx context.Context, p archive.Plan, operation string, d archive.SQLiteDatabase) error {
	if p.Removal != nil {
		return h.removalFence(ctx, p, operation, d)
	}
	permit, err := h.service.Store.ReadReplicaPermit(ctx, d.Binding.DatabaseID)
	if err != nil || permit.FenceState != "held" {
		return archive.ErrRefused
	}
	held := 0
	for _, f := range permit.Fences {
		if f.State != data.FenceHeld {
			continue
		}
		if f.OperationID != operation || f.DatabaseID != d.Binding.DatabaseID || f.IncarnationID != p.Candidate.Incarnation {
			return archive.ErrRefused
		}
		held++
	}
	if held != 1 {
		return archive.ErrRefused
	}
	return nil
}

func (h archiveHost) originalArchiveCredentials(ctx context.Context, id data.ArchiveID, b data.ReplicaBinding) (restore.Credentials, error) {
	p, err := h.service.Store.LoadArchivePlanByArchive(ctx, id)
	if err != nil {
		return restore.Credentials{}, err
	}
	if p.Removal == nil {
		return h.archiveCredentials(ctx, b)
	}
	receipt, err := h.removalCredentials(ctx, p, b)
	if err != nil {
		return restore.Credentials{}, err
	}
	return readDeliveredCredentials(h.stateRoot, receipt)
}

func (h archiveHost) sqliteArchiveCredentialHealth(ctx context.Context, p archive.Plan, variant archive.Database) ([]archive.CredentialHealth, error) {
	d, ok := variant.SQLite()
	if !ok {
		return nil, archive.ErrRefused
	}
	out := []archive.CredentialHealth{}
	if !d.Written {
		return out, nil
	}
	for _, binding := range d.Epochs {
		out = append(out, h.archiveBindingHealth(ctx, p, binding))
	}
	return out, nil
}

func (h archiveHost) archiveBindingHealth(ctx context.Context, p archive.Plan, b data.ReplicaBinding) archive.CredentialHealth {
	health := archive.CredentialHealth{BindingID: b.BindingID, EpochID: b.EpochID}
	files := backupcredentials.Files{Root: filepath.Join(h.stateRoot, "credentials")}
	ref, version := "", uint64(0)
	renewed, err := h.service.Store.ReadArchiveCredentialReceipt(ctx, p.ArchiveID, b.BindingID, b.EpochID)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		original, readErr := h.removalCredentials(ctx, p, b)
		if readErr != nil {
			health.Missing = true
			break
		}
		health.ReceivedAt, health.ExpiresAt = original.ReceivedAt, original.ExpiresAt
		ref, version = original.CredentialRef, original.Version
	case err != nil:
		health.Missing = true
	default:
		sealed, readErr := files.ArchiveReceipt(renewed.Plan.Scope.FileReference(), renewed.Plan.Version)
		if readErr != nil || !reflect.DeepEqual(sealed, renewed) {
			health.Missing = true
			break
		}
		health.ReceivedAt, health.ExpiresAt = renewed.ReceivedAt, renewed.Plan.ExpiresAt
		ref, version = renewed.Plan.Scope.FileReference(), renewed.Plan.Version
	}
	if !health.Missing {
		packet, readErr := files.Read(ref, version)
		health.Missing = readErr != nil
		packet.Clear()
	}
	now := time.Now().UTC()
	if !health.ReceivedAt.IsZero() && now.After(health.ReceivedAt) {
		health.AgeSeconds = int64(now.Sub(health.ReceivedAt) / time.Second)
	}
	if health.ExpiresAt != nil {
		health.Expired = !now.Before(*health.ExpiresAt)
		health.RenewalNeeded = !now.Add(3 * time.Minute).Before(*health.ExpiresAt)
	}
	health.RenewalNeeded = health.RenewalNeeded || health.Missing
	return health
}

func (h archiveHost) archiveMaintenanceDatabase(ctx context.Context, p archive.Plan, m archive.Manifest, d archive.Database, pol policy.Policy) (bool, error) {
	if native, ok := d.Native(); ok {
		return h.nativeArchiveMaintenanceAdmission(ctx, p, m, native, pol)
	}
	return h.sqliteArchiveMaintenanceAdmission(ctx, p, m, d, pol)
}
func (h archiveHost) archiveDatabaseHealth(ctx context.Context, p archive.Plan, d archive.Database) ([]archive.CredentialHealth, error) {
	if native, ok := d.Native(); ok {
		return h.nativeArchiveCredentialHealth(ctx, p, native)
	}
	return h.sqliteArchiveCredentialHealth(ctx, p, d)
}
