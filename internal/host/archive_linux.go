//go:build linux

package host

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"time"

	"github.com/ShaulLavo/brine/internal/apply"
	"github.com/ShaulLavo/brine/internal/archive"
	"github.com/ShaulLavo/brine/internal/backupcredentials"
	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/localexec"
	"github.com/ShaulLavo/brine/internal/replication"
	"github.com/ShaulLavo/brine/internal/restore"
	"github.com/ShaulLavo/brine/internal/store"
	"github.com/ShaulLavo/brine/internal/target"
)

func newArchiveService(s Service, stateRoot, home string, session localexec.Session, executor localexec.Executor, routes apply.RemovalRoutes, units archiveRemovalUnits) archive.Service {
	return archive.Service{Journal: s.Store, Host: archiveHost{service: s, stateRoot: stateRoot, session: session, executor: executor, home: home, routes: routes, units: units, recoveryVerified: make(map[string]time.Time)}, Requester: s.Requester, Authorize: func(ctx context.Context) error {
		_, err := s.Policy.Load(ctx)
		return err
	}, Lock: func(ctx context.Context) (func() error, error) {
		lock, err := s.Store.AcquireHostLock(ctx)
		if err != nil {
			return nil, err
		}
		return lock.Release, nil
	}}
}

func (h archiveHost) Facts(ctx context.Context, app string) (archive.Plan, error) {
	return h.archiveFacts(ctx, app, "", true)
}
func (h archiveHost) archiveFacts(ctx context.Context, app, owner string, requireRetention bool) (archive.Plan, error) {
	var p archive.Plan
	pol, err := h.service.Policy.Load(ctx)
	if err != nil {
		return p, err
	}
	var c archive.Candidate
	if owner == "" {
		c, err = h.service.Store.ArchiveCandidate(ctx, app)
	} else {
		c, err = h.service.Store.ArchiveOwnedCandidate(ctx, app, owner)
	}
	if err != nil {
		return p, err
	}
	snapshot, err := h.service.Inventory.Collect(ctx)
	if err != nil || snapshot.Generation.Status != target.KnownStatus || snapshot.Generation.Value == nil || snapshot.Apps.Status != target.KnownStatus || snapshot.Apps.Value == nil {
		return p, archive.ErrRefused
	}
	if c.ReleaseID != "" {
		var writer store.WriterSchema
		if owner == "" {
			writer, err = h.service.Store.ReadWriterSchema(ctx, c.Incarnation)
		} else {
			writer, err = h.service.Store.ReadArchiveWriterSchema(ctx, c.Incarnation, owner)
		}
		if err != nil || writer.ReleaseID != c.ReleaseID {
			return p, archive.ErrRefused
		}
		matched := false
		for _, observed := range *snapshot.Apps.Value {
			if observed.Name == app {
				if observed.QuadletUnits.Status != target.KnownStatus || observed.QuadletUnits.Value == nil || !reflect.DeepEqual(*observed.QuadletUnits.Value, writer.Units) {
					return p, archive.ErrRefused
				}
				matched = true
			}
		}
		if !matched {
			return p, archive.ErrRefused
		}
	} else {
		for _, observed := range *snapshot.Apps.Value {
			if observed.Name == app {
				return p, archive.ErrRefused
			}
		}
	}
	for i := range c.Databases {
		database, sqlite := c.Databases[i].SQLite()
		if !sqlite {
			return p, archive.ErrRefused
		}
		if !slices.Contains(pol.PersistentRoots(), string(database.Binding.Root)) || database.Allocation == nil || data.VerifyArchiveAllocation(database.Binding, *database.Allocation) != nil {
			return p, archive.ErrRefused
		}
		root, err := data.InspectRoot(string(database.Binding.Root))
		if err == nil {
			root, err = root.WithPreparationProof(database.Allocation.RootProof)
		}
		if err != nil || !root.Admits(database.Binding.Root, 0) {
			return p, archive.ErrRefused
		}
		for _, epoch := range database.Epochs {
			destination, ok := pol.BackupDestination(epoch.Destination.Reference)
			if !ok || destination != epoch.Destination {
				return p, archive.ErrRefused
			}
			evidence, ok := pol.BackupRetention(destination.Reference)
			if requireRetention && (!ok || !evidence.Admits(destination, time.Now().UTC())) {
				return p, archive.ErrRefused
			}
			database.Retention = append(database.Retention, evidence)
			if epoch.Committed {
				artifact, hash, err := (replicaRotation{state: h.service.Store, stateRoot: h.stateRoot, home: h.home}).artifact(ctx, epoch)
				if err != nil || hash != epoch.UnitSHA256 || data.VerifyRunnerFile(artifact.ServicePath) != nil {
					return p, archive.ErrRefused
				}
				raw, err := os.ReadFile(artifact.ServicePath)
				if err != nil || string(raw) != string(artifact.Service) {
					return p, archive.ErrRefused
				}
			}
		}
		c.Databases[i] = archive.SQLiteDatabaseVariant(database)
	}
	identity, err := json.Marshal(snapshot.Identity)
	if err != nil {
		return p, err
	}
	sum := sha256.Sum256(identity)
	p.Candidate = c
	p.PolicyHash = pol.Hash()
	p.TargetHash = "sha256:" + hex.EncodeToString(sum[:])
	p.Generation = *snapshot.Generation.Value
	return p, nil
}

func (h archiveHost) archiveCredentials(ctx context.Context, b data.ReplicaBinding) (restore.Credentials, error) {
	var credentials restore.Credentials
	receipt, err := h.service.Store.CredentialReceipt(ctx, b.BindingID, b.CredentialVersion)
	if err != nil || receipt.EpochID != b.EpochID || receipt.CredentialRef != b.Destination.CredentialRef || receipt.Destination != b.Destination.Reference {
		return credentials, archive.ErrRefused
	}
	files := backupcredentials.Files{Root: filepath.Join(h.stateRoot, "credentials")}
	packet, err := files.Read(receipt.CredentialRef, receipt.Version)
	if err != nil {
		return credentials, archive.ErrRefused
	}
	defer packet.Clear()
	credentials.ReceivedAt = receipt.ReceivedAt
	if receipt.ExpiresAt != nil {
		credentials.ExpiresAt = *receipt.ExpiresAt
	}
	environment := packet.Environment()
	defer clear(environment)
	for _, entry := range environment {
		key, value, _ := strings.Cut(entry, "=")
		switch key {
		case "AWS_ACCESS_KEY_ID":
			credentials.AccessKey = value
		case "AWS_SECRET_ACCESS_KEY":
			credentials.SecretKey = value
		case "AWS_SESSION_TOKEN":
			credentials.SessionToken = value
		}
	}
	return credentials, nil
}

func (h archiveHost) coverSQLite(ctx context.Context, p archive.Plan, d archive.SQLiteDatabase, operation string, writersAt time.Time) (archive.SQLiteCoverage, error) {
	coverage := archive.SQLiteCoverage{DatabaseID: d.Binding.DatabaseID, WritersQuiescedAt: writersAt}
	if d.Allocation == nil || data.VerifyArchiveAllocation(d.Binding, *d.Allocation) != nil {
		return coverage, archive.ErrRefused
	}
	if !d.Written {
		fresh, observed, err := data.FreshArchiveAllocation(d.Binding, *d.Allocation)
		if err != nil {
			return coverage, err
		}
		coverage.Empty = &fresh
		coverage.Observation = observed
		return coverage, nil
	}
	observed := data.ObserveSQLiteSchema(ctx, d.Binding, d.Definitions)
	if observed.State != data.VerifiedEmpty && observed.State != data.VerifiedSchema {
		return coverage, archive.ErrRefused
	}
	coverage.Observation = observed
	if len(d.Epochs) != 1 {
		return coverage, archive.ErrRefused
	}
	b := d.Epochs[0]
	credentials, err := h.archiveCredentials(ctx, b)
	if err != nil {
		return coverage, err
	}
	defer func() { credentials = restore.Credentials{} }()
	binding := restore.Binding{ID: string(b.BindingID), Epoch: string(b.EpochID), CredentialRef: b.Destination.CredentialRef, Destination: restore.Destination{Endpoint: b.Destination.Endpoint, Region: b.Destination.Region, Bucket: b.Destination.Bucket, Prefix: strings.TrimSuffix(b.RemotePrefix, "/"), PathStyle: b.Destination.PathStyle}}
	seed := sha256.Sum256([]byte(string(p.ArchiveID) + ":" + string(d.Binding.DatabaseID)))
	point := data.SQLiteRestorePoint{ID: hex.EncodeToString(seed[:16]), BindingID: b.BindingID, EpochID: b.EpochID, Schema: observed}
	if b.Committed {
		rotation := replicaRotation{state: h.service.Store, stateRoot: h.stateRoot, home: h.home}
		artifact, _, err := rotation.artifact(ctx, b)
		if err != nil {
			return coverage, err
		}
		barrier, err := replication.SyncBarrier(ctx, h.executor, artifact.Binding, 2*time.Minute)
		if err != nil {
			return coverage, err
		}
		point.Kind = data.RestorePointLTX
		point.LTX = &data.RestoreLTXPoint{TXID: barrier.TXID, Barrier: data.RestoreUploadBarrier{BindingID: b.BindingID, EpochID: b.EpochID, TXID: barrier.TXID, ReplicaTXID: barrier.ReplicaTXID, ObservedAt: barrier.ObservedAt}}
	} else {
		name, err := replication.ServiceName(string(b.BindingID))
		if err != nil {
			return coverage, err
		}
		stopped, err := h.serviceStopped(ctx, name, true)
		if err != nil || !stopped {
			return coverage, archive.ErrRefused
		}
		raw, err := h.snapshot(ctx, d, point.ID)
		if err != nil {
			return coverage, err
		}
		defer clear(raw)
		source, _, err := restore.UploadSnapshot(ctx, binding, credentials, point.ID, raw, 3*time.Minute, false)
		if err != nil {
			return coverage, err
		}
		point.Kind = data.RestorePointSnapshot
		point.Snapshot = &data.RestoreSnapshotPoint{ObjectKey: source.ObjectKey, SHA256: source.SHA256, Size: source.Size}
	}
	point.RecordedAt = time.Now().UTC()
	request := requestAtPoint(restore.SQLiteRequest{OperationID: operation + "-" + string(d.Binding.DatabaseID), Budget: 3 * time.Minute}, point)
	request.Coverage = &restore.CoverageEvidence{Source: request.Source, CoveredThrough: writersAt}
	for _, invariant := range d.Invariants {
		request.Invariants = append(request.Invariants, restore.Invariant{Kind: restore.InvariantKind(invariant.Kind), Table: invariant.Table, Column: invariant.Column, Count: invariant.Count, Minimum: invariant.Minimum, Maximum: invariant.Maximum})
	}
	if err = ensurePrivateChild(h.stateRoot, "archive-restores"); err != nil {
		return coverage, err
	}
	engine := h.archiveRestoreEngine(d, binding, credentials)
	receipt, err := restore.VerifySQLite(ctx, &engine, request)
	if err != nil {
		return coverage, err
	}
	coverage.Point = &point
	coverage.Receipt = &receipt
	return coverage, nil
}

func (h archiveHost) snapshot(ctx context.Context, d archive.SQLiteDatabase, id string) ([]byte, error) {
	if err := ensurePrivateChild(h.stateRoot, "archive-snapshots"); err != nil {
		return nil, err
	}
	target := data.SQLiteCaptureTarget(data.SQLiteCaptureRequest{Binding: d.Binding, StagingRoot: filepath.Join(h.stateRoot, "archive-snapshots"), PointID: id, MaxBytes: data.MaxSQLiteCaptureBytes})
	captured, err := data.CaptureEngine(ctx, target, data.MaxCaptureBudget)
	if err != nil {
		return nil, err
	}
	raw, ok := captured.SQLite()
	if !ok {
		return nil, archive.ErrRefused
	}
	return raw, nil
}

func (h archiveHost) archiveRestoreEngine(d archive.SQLiteDatabase, binding restore.Binding, credentials restore.Credentials) restore.SQLiteEngine {
	return restore.SQLiteEngine{Root: filepath.Join(h.stateRoot, "archive-restores"), MaxSnapshotBytes: 64 << 20, Observer: restoreSchemaObserver{database: d.Binding.Name, definitions: d.Definitions}, Bindings: restore.BindingReaderFunc(func(_ context.Context, id, epoch string) (restore.Binding, error) {
		if id != binding.ID || epoch != binding.Epoch {
			return restore.Binding{}, archive.ErrRefused
		}
		return binding, nil
	}), Credentials: restore.CredentialReaderFunc(func(_ context.Context, ref string) (restore.Credentials, error) {
		if ref != binding.CredentialRef {
			return restore.Credentials{}, archive.ErrRefused
		}
		return credentials, nil
	})}
}
