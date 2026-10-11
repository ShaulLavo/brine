//go:build linux

package store

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/ShaulLavo/brine/internal/archive"
	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/replication"
)

type storedRecoveryHost struct {
	state *Store
	lock  string
}

func (h storedRecoveryHost) Facts(context.Context, string) (archive.Plan, error) {
	return archive.Plan{}, ErrInvalid
}
func (h storedRecoveryHost) StopWriters(context.Context, archive.Plan) error  { return ErrInvalid }
func (h storedRecoveryHost) StopReplicas(context.Context, archive.Plan) error { return ErrInvalid }
func (h storedRecoveryHost) WritersStopped(context.Context, archive.Plan) (bool, error) {
	return false, ErrInvalid
}
func (h storedRecoveryHost) ReplicasStopped(context.Context, archive.Plan) (bool, error) {
	return false, ErrInvalid
}
func (h storedRecoveryHost) Cover(context.Context, archive.Plan, archive.Database, string, time.Time) (archive.Coverage, error) {
	return archive.Coverage{}, ErrInvalid
}
func (h storedRecoveryHost) AcquireLifetimeLocks(ctx context.Context, _ archive.Plan) (func() error, error) {
	lock, err := replication.AcquireLifetimeLock(ctx, h.lock)
	if err != nil {
		return nil, err
	}
	return lock.Release, nil
}
func (h storedRecoveryHost) InspectArchiveRuntime(context.Context, archive.Plan) (archive.RuntimeInspection, error) {
	return archive.RuntimeInspection{Complete: true}, nil
}
func (h storedRecoveryHost) ReconcileArchiveRuntime(context.Context, archive.Plan) error { return nil }
func (h storedRecoveryHost) CheckArchiveRecovery(ctx context.Context, p archive.Plan, o archive.Operation, _ archive.Manifest) error {
	candidate, err := h.state.ArchiveOwnedCandidate(ctx, p.Candidate.App, o.ID)
	if err != nil {
		return err
	}
	if len(candidate.Databases) != len(p.Candidate.Databases) {
		return ErrConflict
	}
	for i := range candidate.Databases {
		candidate.Databases[i].Retention = p.Candidate.Databases[i].Retention
	}
	if !reflect.DeepEqual(candidate, p.Candidate) {
		return ErrConflict
	}
	for _, d := range candidate.Databases {
		if data.VerifyArchivedAllocation(d.Binding, *d.Allocation, p.ArchiveID) == nil {
			err = data.VerifyArchivedEmpty(d.Binding, *d.Allocation, p.ArchiveID)
		} else {
			_, _, err = data.FreshArchiveAllocation(d.Binding, *d.Allocation)
		}
		if err != nil {
			return err
		}
	}
	return nil
}
func TestArchiveReconcileDurableJournalAndFilesystemAfterReopen(t *testing.T) {
	for _, boundary := range []string{"cleanup_intent", "runtime_retired", "rename_intent", "rename_effect", "renamed"} {
		t.Run(boundary, func(t *testing.T) {
			ctx := context.Background()
			home := stateDir(t)
			s, err := Open(home)
			if err != nil {
				t.Fatal(err)
			}
			request := dataRequest()
			root := t.TempDir()
			// #nosec G302 -- Private fixture root requires owner traversal.
			if err = os.Chmod(root, 0700); err != nil {
				t.Fatal(err)
			}
			request.Database.PersistentRoot = data.PersistentRoot(root)
			reserved, err := s.ReserveDatabase(ctx, request)
			if err != nil {
				t.Fatal(err)
			}
			if err = os.MkdirAll(filepath.Join(root, reserved.Database.RelativeDirectory), 0700); err != nil {
				t.Fatal(err)
			}
			allocation, err := data.CaptureAllocation(reserved.Database)
			if err != nil {
				t.Fatal(err)
			}
			if err = s.RecordAllocation(ctx, allocation); err != nil {
				t.Fatal(err)
			}
			candidate, err := s.ArchiveCandidate(ctx, request.App)
			if err != nil {
				t.Fatal(err)
			}
			now := time.Now().UTC()
			candidate.Databases[0].Retention = []data.RetentionEvidence{{Destination: request.Destination.Reference, Endpoint: request.Destination.Endpoint, Bucket: request.Destination.Bucket, BasePrefix: request.Destination.BasePrefix, VerificationID: strings.Repeat("f", 32), VerifiedAt: now.Format(time.RFC3339Nano), FreshnessSeconds: 3600, NoObjectExpiration: true}}
			id, err := data.NewID()
			if err != nil {
				t.Fatal(err)
			}
			tree, err := data.CaptureArchiveTree(reserved.Database.Root, candidate.Incarnation, data.ArchiveID(id))
			if err != nil {
				t.Fatal(err)
			}
			p := archive.Plan{ArchiveID: data.ArchiveID(id), Requester: "deploy:" + strings.Repeat("a", 64), PolicyHash: request.PolicyHash, TargetHash: "sha256:" + strings.Repeat("b", 64), CreatedAt: now, Candidate: candidate, Trees: []data.ArchiveTree{tree}}
			p.ID, err = p.Fingerprint()
			if err != nil || !p.Valid() {
				t.Fatal(err)
			}
			if err = s.SaveArchivePlan(ctx, p); err != nil {
				t.Fatal(err)
			}
			p, o, m := preparedStoredArchive(t, s, p)
			var disposition archive.Disposition
			for _, stage := range []archive.DispositionStage{archive.CleanupIntent, archive.RuntimeRetired, archive.RenameIntent, archive.Renamed} {
				if stage == archive.Renamed {
					if boundary == "rename_intent" {
						break
					}
					if err = data.RenameArchiveTree(tree, p.Candidate.Incarnation, p.ArchiveID); err != nil {
						t.Fatal(err)
					}
					if boundary == "rename_effect" {
						break
					}
				}
				next := archive.Disposition{ArchiveID: p.ArchiveID, Stage: stage, Sequence: disposition.Sequence + 1}
				disposition, err = s.AdvanceArchiveDisposition(ctx, o, disposition, next)
				if err != nil {
					t.Fatal(err)
				}
				if string(stage) == boundary {
					break
				}
			}
			if err = s.Close(); err != nil {
				t.Fatal(err)
			}
			s, err = Open(home)
			if err != nil {
				t.Fatal(err)
			}
			defer func() {
				if err := s.Close(); err != nil {
					t.Error(err)
				}
			}()
			h := storedRecoveryHost{state: s, lock: filepath.Join(home, "replica.lock")}
			service := archive.Service{Journal: s, Host: h, Requester: p.Requester, Authorize: func(context.Context) error { return nil }, Lock: func(context.Context) (func() error, error) { return func() error { return nil }, nil }}
			out, err := service.Reconcile(ctx, p.ArchiveID)
			if err != nil || out.Stage != archive.Retired {
				t.Fatal("reopen failed to settle", out, err)
			}
			read, exists, err := s.ReadArchiveDisposition(ctx, p.ArchiveID)
			if err != nil || !exists || read != out {
				t.Fatal(read, err)
			}
			retained, err := s.ReadArchiveManifest(ctx, p.ArchiveID)
			if err != nil || !reflect.DeepEqual(retained, m) {
				t.Fatal("manifest changed", err)
			}
			permit, err := s.ReadReplicaPermit(ctx, reserved.Database.DatabaseID)
			if err != nil || permit.FenceState != "held" {
				t.Fatal("retired startup fence cleared", permit, err)
			}
			recreated, err := s.ReserveDatabase(ctx, request)
			if err != nil || recreated.Database.IncarnationID == reserved.Database.IncarnationID || recreated.Replica.RemotePrefix == reserved.Replica.RemotePrefix {
				t.Fatal("reused archived identity", recreated, err)
			}
			if repeated, err := service.Reconcile(ctx, p.ArchiveID); err != nil || repeated != out {
				t.Fatal("terminal receipt touched recreation", repeated, err)
			}
			if err = data.VerifyArchivedTree(tree, p.Candidate.Incarnation, p.ArchiveID); err != nil {
				t.Fatal(err)
			}
		})
	}
}
