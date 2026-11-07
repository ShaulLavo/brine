//go:build linux

package host

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"reflect"
	"slices"
	"time"

	"github.com/ShaulLavo/brine/internal/archive"
	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/policy"
	"github.com/ShaulLavo/brine/internal/store"
	"github.com/ShaulLavo/brine/internal/target"
)

func archiveTargetHash(snapshot target.Snapshot) (string, error) {
	raw, err := json.Marshal(snapshot.Identity)
	if err != nil {
		return "", err
	}
	hash := sha256.Sum256(raw)
	return "sha256:" + hex.EncodeToString(hash[:]), nil
}

func (h archiveHost) archiveCandidate(ctx context.Context, app, owner string) (archive.Candidate, []archive.PreparedRef, error) {
	if h.removal && owner == "" {
		return h.service.Store.RemovalCandidate(ctx, app)
	}
	if owner != "" {
		c, err := h.service.Store.ArchiveOwnedCandidate(ctx, app, owner)
		return c, nil, err
	}
	c, err := h.service.Store.ArchiveCandidate(ctx, app)
	return c, nil, err
}

func (h archiveHost) archiveWriterFacts(ctx context.Context, c archive.Candidate, owner string, apps []target.App) error {
	if c.ReleaseID == "" {
		return archiveAppAbsent(apps, c.App)
	}
	var writer store.WriterSchema
	var err error
	if h.removal && owner == "" {
		release, e := h.service.Store.CurrentRelease(ctx, c.App)
		writer.ReleaseID, writer.Units, err = release.ID, release.Units, e
	} else if owner == "" {
		writer, err = h.service.Store.ReadWriterSchema(ctx, c.Incarnation)
	} else {
		writer, err = h.service.Store.ReadArchiveWriterSchema(ctx, c.Incarnation, owner)
	}
	if err != nil || writer.ReleaseID != c.ReleaseID {
		return archive.ErrRefused
	}
	for _, observed := range apps {
		if observed.Name != c.App {
			continue
		}
		if observed.QuadletUnits.Status != target.KnownStatus || observed.QuadletUnits.Value == nil || !reflect.DeepEqual(*observed.QuadletUnits.Value, writer.Units) {
			return archive.ErrRefused
		}
		return nil
	}
	return archive.ErrRefused
}

func (h archiveHost) archiveDatabaseFacts(ctx context.Context, variant archive.Database, pol policy.Policy, requireRetention bool) (archive.SQLiteDatabase, error) {
	d, ok := variant.SQLite()
	if !ok || !slices.Contains(pol.PersistentRoots(), string(d.Binding.Root)) || d.Allocation == nil || data.VerifyArchiveAllocation(d.Binding, *d.Allocation) != nil {
		return d, archive.ErrRefused
	}
	root, err := data.InspectRoot(string(d.Binding.Root))
	if err == nil {
		root, err = root.WithPreparationProof(d.Allocation.RootProof)
	}
	if err != nil || !root.Admits(d.Binding.Root, 0) {
		return d, archive.ErrRefused
	}
	for _, epoch := range d.Epochs {
		destination, ok := pol.BackupDestination(epoch.Destination.Reference)
		if !ok || destination != epoch.Destination {
			return d, archive.ErrRefused
		}
		evidence, known := pol.BackupRetention(destination.Reference)
		if requireRetention && (!known || !evidence.Admits(destination, time.Now().UTC())) {
			return d, archive.ErrRefused
		}
		d.Retention = append(d.Retention, evidence)
		if epoch.Committed && h.archiveReplicaArtifact(ctx, epoch) != nil {
			return d, archive.ErrRefused
		}
	}
	return d, nil
}

func (h archiveHost) archiveReplicaArtifact(ctx context.Context, b data.ReplicaBinding) error {
	artifact, hash, err := (replicaRotation{state: h.service.Store, stateRoot: h.stateRoot, home: h.home}).artifact(ctx, b)
	if err != nil || hash != b.UnitSHA256 || data.VerifyRunnerFile(artifact.ServicePath) != nil {
		return archive.ErrRefused
	}
	raw, err := os.ReadFile(artifact.ServicePath)
	if err != nil || string(raw) != string(artifact.Service) {
		return archive.ErrRefused
	}
	return nil
}

func archiveAppAbsent(apps []target.App, app string) error {
	for _, observed := range apps {
		if observed.Name == app {
			return archive.ErrRefused
		}
	}
	return nil
}
