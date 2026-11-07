//go:build linux

package host

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"time"

	"github.com/ShaulLavo/brine/internal/archive"
	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/sqliteprep"
	"github.com/ShaulLavo/brine/internal/store"
)

func (h archiveHost) HoldPrepared(ctx context.Context, p archive.Plan) (archive.SourceHold, error) {
	if !p.Valid() || p.Removal == nil {
		return nil, archive.ErrRefused
	}
	ids := make([]data.DatabaseID, len(p.Candidate.Databases))
	for i, d := range p.Candidate.Databases {
		ids[i] = d.Reference().DatabaseID
	}
	guard, err := h.service.Store.AcquireSQLiteRemovalGuard(ctx, ids)
	if err != nil {
		return nil, err
	}
	if err = h.bindRemovalAuthority(ctx, p, guard); err != nil {
		return nil, errors.Join(err, guard.Close())
	}
	for _, variant := range p.Candidate.Databases {
		if err = h.attachPreparedSource(ctx, p, variant, guard); err != nil {
			return nil, errors.Join(err, guard.Close())
		}
	}
	return guard, nil
}

func (h archiveHost) bindRemovalAuthority(ctx context.Context, p archive.Plan, g *store.SQLiteRemovalGuard) error {
	pol, err := h.service.Policy.Load(ctx)
	if err != nil {
		return err
	}
	snapshot, err := h.service.Inventory.Collect(ctx)
	if err != nil {
		return err
	}
	target, err := archiveTargetHash(snapshot)
	if err != nil || target != p.TargetHash {
		return archive.ErrRefused
	}
	return g.BindAuthority(pol, target)
}

func (h archiveHost) attachPreparedSource(ctx context.Context, p archive.Plan, variant archive.Database, g *store.SQLiteRemovalGuard) error {
	d, ok := variant.SQLite()
	if !ok || d.Allocation == nil || len(d.Epochs) != 1 {
		return archive.ErrRefused
	}
	permit, err := h.service.Store.ReadReplicaPermit(ctx, d.Binding.DatabaseID)
	if err != nil || !reflect.DeepEqual(permit.Database, d.Binding) || !reflect.DeepEqual(permit.Replica, d.Epochs[0]) || admitSQLiteRoot(ctx, h.service.Store, d.Binding) != nil {
		return archive.ErrRefused
	}
	if err = ensurePrivateChild(h.stateRoot, "replica-locks"); err != nil {
		return err
	}
	child, err := g.Child(d.Binding.DatabaseID)
	if err != nil {
		return err
	}
	hold, err := child.HoldSource(ctx)
	if err != nil {
		return err
	}
	empty, err := h.preparedEmptyExpectation(ctx, p, d)
	if err != nil {
		return errors.Join(err, hold.Release())
	}
	source, err := openArchiveSQLiteSource(p, d, empty, hold)
	if err != nil {
		return errors.Join(err, hold.Release())
	}
	if empty == nil && !d.Written {
		if err = h.qualifyAbsentSource(ctx, p, d, source); err != nil {
			return errors.Join(err, source.Close())
		}
	}
	if err = h.checkPreparedSource(ctx, p, d, source); err != nil {
		return errors.Join(err, source.Close())
	}
	if err = g.AttachSource(ctx, d.Binding.DatabaseID, source); err != nil {
		return errors.Join(err, source.Close())
	}
	return nil
}

func (h archiveHost) CoverPrepared(ctx context.Context, p archive.Plan, ref archive.PreparedRef, operation string, _ time.Time) (archive.Coverage, error) {
	o, exists, err := h.service.Store.ReadArchiveOperation(ctx, p.ID)
	if err != nil || !exists || o.ID != operation {
		return archive.Coverage{}, archive.ErrRefused
	}
	point, claimed, err := h.service.Store.ReadSQLiteRemovalPoint(ctx, p.ID, ref.DatabaseID)
	if err != nil {
		return archive.Coverage{}, err
	}
	for _, variant := range p.Candidate.Databases {
		d, ok := variant.SQLite()
		if !ok || d.Binding.DatabaseID != ref.DatabaseID {
			continue
		}
		if !d.Written {
			return h.Cover(ctx, p, variant, operation, time.Now().UTC())
		}
		coverage := archive.SQLiteCoverage{DatabaseID: ref.DatabaseID, Point: &point.Source.Point, Receipt: &point.Verification, Observation: point.Source.Point.Schema, WritersQuiescedAt: point.Source.Cutoff, Prepared: &archive.PreparedCoverage{Ref: ref, ClaimedAt: claimed}}
		out := archive.SQLiteCoverageVariant(coverage)
		if !out.Admits(p, variant, operation, time.Now().UTC()) {
			return archive.Coverage{}, archive.ErrRefused
		}
		return out, nil
	}
	return archive.Coverage{}, archive.ErrRefused
}

func (h archiveHost) removalCredentialBinding(p archive.Plan, b data.ReplicaBinding) (data.ReplicaBinding, error) {
	if p.Removal == nil {
		return b, nil
	}
	for _, ref := range p.Removal.Points {
		if ref.DatabaseID != b.DatabaseID {
			continue
		}
		if ref.CredentialVersion == 0 {
			return b, archive.ErrRefused
		}
		b.CredentialVersion = ref.CredentialVersion
		return b, nil
	}
	return b, archive.ErrRefused
}

func (h archiveHost) removalCredentials(ctx context.Context, p archive.Plan, b data.ReplicaBinding) (store.CredentialRecord, error) {
	if p.Removal != nil {
		return h.service.Store.ReadSQLiteRemovalCredential(ctx, p.ID, b.DatabaseID)
	}
	record, err := h.service.Store.CredentialReceipt(ctx, b.BindingID, b.CredentialVersion)
	if err != nil || b.CredentialVersion == 0 || record.App != p.Candidate.App || record.Version != b.CredentialVersion || record.EpochID != b.EpochID || record.CredentialRef != b.Destination.CredentialRef || record.Destination != b.Destination.Reference {
		return store.CredentialRecord{}, archive.ErrRefused
	}
	return record, nil
}

func (h archiveHost) removalFence(ctx context.Context, p archive.Plan, operation string, d archive.SQLiteDatabase) error {
	fence, err := h.service.Store.SQLiteArchiveFence(ctx, p.ArchiveID, d.Binding.DatabaseID)
	if err != nil || fence.State != data.FenceHeld || fence.IncarnationID != p.Candidate.Incarnation {
		return archive.ErrRefused
	}
	for _, ref := range p.Removal.Points {
		if ref.DatabaseID != d.Binding.DatabaseID {
			continue
		}
		if fence != ref.Fence {
			return archive.ErrRefused
		}
		return nil
	}
	if fence.OperationID != operation {
		return archive.ErrRefused
	}
	return nil
}

func openArchiveSQLiteSource(p archive.Plan, d archive.SQLiteDatabase, empty *sqliteprep.EmptyAllocation, hold *store.SQLiteSourceHold) (*sqliteSource, error) {
	if data.VerifyArchiveAllocation(d.Binding, *d.Allocation) == nil {
		return openSQLiteSource(d.Binding, d.Definitions, empty, hold)
	}
	if data.VerifyArchivedAllocation(d.Binding, *d.Allocation, p.ArchiveID) != nil {
		return nil, archive.ErrRefused
	}
	return openArchivedSQLiteSource(p, d, empty, hold)
}

func openArchivedSQLiteSource(p archive.Plan, d archive.SQLiteDatabase, empty *sqliteprep.EmptyAllocation, hold *store.SQLiteSourceHold) (out *sqliteSource, err error) {
	s := &sqliteSource{empty: empty, rootPath: string(d.Binding.Root), components: []string{"archives", string(p.ArchiveID), "databases", string(d.Binding.DatabaseID)}, filename: string(d.Binding.Filename), database: d.Binding.Name, databaseID: d.Binding.DatabaseID, definitions: d.Definitions, members: map[string]*os.File{}, proofs: map[string]memberProof{}, hold: hold}
	defer func() {
		if err != nil {
			err = errors.Join(err, s.closeHandles())
		}
	}()
	if s.root, err = openPathNoFollow(s.rootPath); err != nil {
		return nil, err
	}
	if s.dir, err = s.walk(); err != nil {
		return nil, err
	}
	if s.location, err = s.observeLocation(); err != nil {
		return nil, err
	}
	for _, suffix := range []string{"", "-wal", "-journal"} {
		if err = s.retain(s.filename + suffix); err != nil {
			return nil, err
		}
	}
	return s, nil
}

var _ archive.RemovalHost = archiveHost{}

func removalWriterObservation(ctx context.Context, h archiveHost, p archive.Plan) error {
	stopped, err := h.WritersStopped(ctx, p)
	if err != nil || !stopped {
		return archive.ErrRefused
	}
	sources := make([]string, 0, len(p.Candidate.Databases))
	for _, d := range p.Candidate.Databases {
		binding, _ := d.SQLite()
		sources = append(sources, filepath.Join(string(binding.Binding.Root), binding.Binding.RelativeDirectory))
	}
	if !containersOutside(ctx, h.session, sources) {
		return archive.ErrRefused
	}
	return nil
}
func removalReplicaObservation(ctx context.Context, h archiveHost, p archive.Plan) error {
	stopped, err := h.ReplicasStopped(ctx, p)
	if err != nil || !stopped {
		return archive.ErrRefused
	}
	return nil
}

func (h archiveHost) checkPreparedSource(ctx context.Context, p archive.Plan, d archive.SQLiteDatabase, source *sqliteSource) error {
	_, claimed, err := h.service.Store.ReadArchiveOperation(ctx, p.ID)
	if err != nil || !claimed {
		return err
	}
	for _, ref := range p.Removal.Points {
		if ref.DatabaseID != d.Binding.DatabaseID {
			continue
		}
		point, _, err := h.service.Store.ReadSQLiteRemovalPoint(ctx, p.ID, ref.DatabaseID)
		if err != nil {
			return err
		}
		bundle, err := source.Recheck(ctx)
		if err != nil || !point.Stopped.Matches(bundle) {
			return errSQLiteSource
		}
	}
	return nil
}

func (h archiveHost) preparedEmptyExpectation(ctx context.Context, p archive.Plan, d archive.SQLiteDatabase) (*sqliteprep.EmptyAllocation, error) {
	for _, ref := range p.Removal.Points {
		if ref.DatabaseID != d.Binding.DatabaseID {
			continue
		}
		source, err := h.service.Store.ReadSQLitePreparedSource(ctx, p.Candidate.App, string(d.Binding.Name), ref.PreparationID)
		if err != nil {
			return nil, err
		}
		return source.Scope.EmptyAllocation, nil
	}
	return nil, nil
}

func (h archiveHost) qualifyAbsentSource(ctx context.Context, p archive.Plan, d archive.SQLiteDatabase, source *sqliteSource) error {
	allocation, err := h.absentRemovalAllocation(ctx, p, d)
	if err != nil {
		return err
	}
	return qualifyAbsentRemovalSource(source, allocation)
}
