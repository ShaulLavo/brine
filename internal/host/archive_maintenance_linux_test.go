//go:build linux

package host

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ShaulLavo/brine/internal/archive"
	"github.com/ShaulLavo/brine/internal/backupcredentials"
	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/policy"
	"github.com/ShaulLavo/brine/internal/restore"
	"github.com/ShaulLavo/brine/internal/store"
	"github.com/ShaulLavo/brine/internal/target"
)

// This fixture journals retirement and really renames private allocated trees.
// Its systemd/Podman observations are synthetic; it is not a host deployment drill.
func retainedHostFixture(t *testing.T) (archiveHost, archive.Plan, archive.Disposition, *archiveExecutor) {
	t.Helper()
	ctx := context.Background()
	state, desired, facts := persistentHostFixture(t)
	_ = collectPersistent(t, facts, desired)
	raw := fmt.Sprintf(`schema_version=1
version="retained-fixture"
persistent_roots=[%q]
[resources]
memory_mb=512
pids_limit=128
[[backup_destinations]]
reference="primary"
endpoint="https://storage.example"
region="region-1"
bucket="backups"
base_prefix="brine"
credential_ref="primary"
[[backup_retention]]
destination="primary"
endpoint="https://storage.example"
bucket="backups"
base_prefix="brine"
verification_id="11111111111111111111111111111111"
verified_at=%q
freshness_seconds=3600
no_object_expiration=true
`, string(desired.Databases[0].PersistentRoot), time.Now().UTC().Add(-time.Minute).Format(time.RFC3339Nano))
	pol, err := policy.Parse([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	executor := &archiveExecutor{show: archiveStopped}
	h := archiveHostForTest(t, executor)
	h.stateRoot = t.TempDir()
	if err = os.Chmod(h.stateRoot, 0700); err != nil { // #nosec G302 -- Private fixture directory requires owner traversal.
		t.Fatal(err)
	}
	h.service = Service{Store: state, Requester: "deploy:" + strings.Repeat("a", 64), Policy: &fakePolicy{p: pol}, Inventory: &fakeInventory{store: state, snapshot: target.Snapshot{Generation: target.Known(uint64(0)), Apps: target.Known([]target.App{})}}}
	service := newArchiveService(h.service, h.stateRoot, t.TempDir(), h.session, executor, nil, nil)
	p, err := service.Plan(ctx, string(desired.Name))
	if err != nil {
		t.Fatal("plan", err)
	}
	operation, err := state.ClaimArchive(ctx, p)
	if err != nil {
		t.Fatal(err)
	}
	for _, stage := range []archive.Stage{archive.WritersStopIntent, archive.WritersStopped, archive.CoverageIntent} {
		operation, err = state.AdvanceArchive(ctx, operation, stage)
		if err != nil {
			t.Fatal(err)
		}
	}
	cutoff := time.Now().UTC()
	coverage, err := h.Cover(ctx, p, p.Candidate.Databases[0], operation.ID, cutoff)
	if err != nil {
		t.Fatal(err)
	}
	operation, err = state.SaveArchiveCoverage(ctx, operation, []archive.Coverage{coverage})
	if err != nil {
		t.Fatal(err)
	}
	for _, stage := range []archive.Stage{archive.ReplicaStopIntent, archive.Quiesced} {
		operation, err = state.AdvanceArchive(ctx, operation, stage)
		if err != nil {
			t.Fatal(err)
		}
	}
	manifest := archive.Manifest{Plan: p, OperationID: operation.ID, Coverage: []archive.Coverage{coverage}, QuiescedAt: time.Now().UTC(), PreparedAt: time.Now().UTC()}
	operation, err = state.SaveArchiveManifest(ctx, operation, manifest)
	if err != nil {
		t.Fatal(err)
	}
	cursor := archive.Disposition{}
	for _, stage := range []archive.DispositionStage{archive.CleanupIntent, archive.RuntimeRetired, archive.RenameIntent, archive.Renamed} {
		if stage == archive.Renamed {
			if err = data.RenameArchiveTree(p.Trees[0], p.Candidate.Incarnation, p.ArchiveID); err != nil {
				t.Fatal(err)
			}
		}
		next := archive.Disposition{ArchiveID: p.ArchiveID, Sequence: cursor.Sequence + 1, Stage: stage}
		cursor, err = state.AdvanceArchiveDisposition(ctx, operation, cursor, next)
		if err != nil {
			t.Fatal(err)
		}
	}
	retired := archive.Disposition{ArchiveID: p.ArchiveID, Sequence: cursor.Sequence + 1, Stage: archive.Retired, CommittedAt: time.Now().UTC()}
	retired.RetainUntil = retired.CommittedAt.Add(30 * 24 * time.Hour)
	if _, err = state.FinishArchiveDisposition(ctx, operation, cursor, retired); err != nil {
		t.Fatal(err)
	}
	return h, p, retired, executor
}

func TestArchiveMaintenanceRealRootAdmissionAndFreshIncarnationIsolation(t *testing.T) {
	h, p, d, e := retainedHostFixture(t)
	ctx := context.Background()
	if _, err := h.ArchiveMaintenanceAdmission(ctx, p, d); err != nil {
		t.Fatal("prepared retired root refused", err)
	}
	original := sqliteArchiveDatabase(t, p.Candidate.Databases[0])
	reserved, err := h.service.Store.ReserveDatabase(ctx, store.DataReservation{App: p.Candidate.App, PolicyHash: p.PolicyHash, Database: data.SQLiteDatabase{Name: original.Binding.Name, PersistentRoot: original.Binding.Root, MountPath: original.Binding.MountPath, Filename: original.Binding.Filename, BackupDestination: original.Epochs[0].Destination.Reference, SyncInterval: time.Minute}, Destination: original.Epochs[0].Destination})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = data.PrepareDirectory(reserved.Database); err != nil {
		t.Fatal(err)
	}
	if reserved.Database.IncarnationID == p.Candidate.Incarnation {
		t.Fatal("fresh incarnation reused archive identity")
	}
	id := strings.Repeat("a", 64)
	raw, err := json.Marshal([]map[string]any{{"Id": id, "State": map[string]any{"Running": true, "Pid": 101}, "Mounts": []map[string]string{{"Source": filepath.Join(string(reserved.Database.Root), reserved.Database.RelativeDirectory)}}}})
	if err != nil {
		t.Fatal(err)
	}
	e.listed, e.inspected = id, string(raw)
	if _, err = h.ArchiveMaintenanceAdmission(ctx, p, d); err != nil {
		t.Fatal("unrelated recreated writer blocked old archive maintenance", err)
	}
	manifest, err := h.service.Store.ReadArchiveManifest(ctx, p.ArchiveID)
	if err != nil {
		t.Fatal(err)
	}
	proof, err := h.VerifyArchiveRetention(ctx, p, manifest, d)
	if err != nil || !archive.RetentionProofMatches(proof, p, d, manifest, time.Now().UTC()) {
		t.Fatal("never-written retained proof", proof, err)
	}
	inventory := h.service.Inventory.(*fakeInventory)
	inventory.snapshot.Identity.ID = "changed-fixture"
	if _, err = h.ArchiveMaintenanceAdmission(ctx, p, d); !errors.Is(err, archive.ErrRefused) {
		t.Fatal("target drift admitted", err)
	}
}

func TestArchiveMaintenanceOriginalReplicaAndMountAdversaries(t *testing.T) {
	h, p, _, e := retainedHostFixture(t)
	ctx := context.Background()
	for _, show := range []string{strings.Replace(archiveStopped, "MainPID=0", "MainPID=123", 1), strings.Replace(archiveStopped, "ControlPID=0", "ControlPID=123", 1), strings.Replace(archiveStopped, "Job=", "Job=17", 1), strings.Replace(archiveStopped, "ControlGroup=", "ControlGroup=/job", 1), "unknown"} {
		e.show = show
		if err := h.maintenanceQuiescent(ctx, p); !errors.Is(err, archive.ErrRefused) || e.stopCalls != 0 {
			t.Fatal("unsafe original replica observation admitted or stopped", show, err)
		}
	}
	e.show = archiveStopped
	id := strings.Repeat("a", 64)
	for _, relative := range []string{p.Trees[0].SourceRelative, p.Trees[0].ArchiveRelative} {
		raw, err := json.Marshal([]map[string]any{{"Id": id, "State": map[string]any{"Running": true, "Pid": 123}, "Mounts": []map[string]string{{"Source": filepath.Join(string(p.Trees[0].Root), relative)}}}})
		if err != nil {
			t.Fatal(err)
		}
		e.listed, e.inspected = id, string(raw)
		if err = h.maintenanceQuiescent(ctx, p); !errors.Is(err, archive.ErrRefused) || e.stopCalls != 0 {
			t.Fatal("old or archive mount admitted", relative, err)
		}
	}
	e.listed, e.inspected = "", ""
	unlock, err := h.AcquireLifetimeLocks(ctx, p)
	if err != nil {
		t.Fatal(err)
	}
	canceled, cancel := context.WithTimeout(ctx, 25*time.Millisecond)
	defer cancel()
	if release, err := h.AcquireLifetimeLocks(canceled, p); err == nil {
		_ = release()
		t.Fatal("held original lifetime lock reacquired")
	}
	if err = unlock(); err != nil {
		t.Fatal(err)
	}
	// The lock is outside the bundle and survives its removal.
	binding := sqliteArchiveDatabase(t, p.Candidate.Databases[0]).Epochs[0]
	if err = data.VerifyRunnerFile(filepath.Join(h.stateRoot, "replica-locks", string(binding.BindingID)+".lock")); err != nil {
		t.Fatal(err)
	}
}

func TestArchiveRetentionEngineIndependentlyRestoresExactOriginalSource(t *testing.T) {
	h, p, _, _ := retainedHostFixture(t)
	database := sqliteArchiveDatabase(t, p.Candidate.Databases[0])
	epoch := database.Epochs[0]
	var payload []byte
	var payloadLock sync.Mutex
	var gets atomic.Int64
	point := strings.Repeat("f", 32)
	expected := "/backups/" + strings.TrimSuffix(epoch.RemotePrefix, "/") + "/restore-points/" + point + "/snapshot.sqlite"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		payloadLock.Lock()
		defer payloadLock.Unlock()
		if r.URL.Path != expected {
			t.Errorf("restored another epoch or point: %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if !strings.Contains(r.Header.Get("Authorization"), "AWS4-HMAC-SHA256") {
			t.Error("request was not signed")
		}
		switch r.Method {
		case http.MethodPut:
			if payload != nil {
				w.WriteHeader(http.StatusPreconditionFailed)
				return
			}
			var err error
			payload, err = io.ReadAll(r.Body)
			if err != nil {
				t.Error(err)
			}
			w.WriteHeader(http.StatusCreated)
		case http.MethodGet:
			gets.Add(1)
			_, _ = w.Write(payload)
		default:
			t.Errorf("unexpected remote operation %s", r.Method)
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
	}))
	defer server.Close()
	remote := restore.Binding{ID: string(epoch.BindingID), Epoch: string(epoch.EpochID), CredentialRef: epoch.Destination.CredentialRef, Destination: restore.Destination{Endpoint: server.URL, Region: "test-region", Bucket: "backups", Prefix: strings.TrimSuffix(epoch.RemotePrefix, "/"), PathStyle: true}}
	credentials := restore.Credentials{AccessKey: "PLANTED_ACCESS", SecretKey: "PLANTED_SECRET", ReceivedAt: time.Now().UTC().Add(-time.Minute)}
	uploadRoot := t.TempDir()
	if err := os.Chmod(uploadRoot, 0700); err != nil { // #nosec G302 -- Private fixture directory requires owner traversal.
		t.Fatal(err)
	}
	cutoff := time.Now().UTC()
	source, _, err := restore.CreateEmptySnapshot(context.Background(), uploadRoot, remote, credentials, point, time.Minute, true)
	if err != nil {
		t.Fatal(err)
	}
	if err = ensurePrivateChild(h.stateRoot, "archive-restores"); err != nil {
		t.Fatal(err)
	}
	engine := h.archiveRestoreEngine(database, remote, credentials)
	engine.AllowHTTPForTests = true
	schema := restore.SQLiteSchemaObservation{State: restore.VerifiedEmpty, Marker: data.EmptyMarker, CatalogSHA256: data.EmptyCatalogSHA256}
	request := restore.SQLiteRequest{OperationID: "archive-retention-test", Budget: time.Minute, Source: restore.SQLiteSource{Kind: restore.SQLiteSnapshot, Snapshot: &source}, ExpectedSchema: schema}
	request.Coverage = &restore.CoverageEvidence{Source: request.Source, CoveredThrough: cutoff}
	receipt, err := engine.Test(context.Background(), request)
	if err != nil || !receipt.Valid() || gets.Load() != 1 || receipt.Schema != schema || receipt.Coverage == nil || !receipt.Coverage.CoveredThrough.Equal(cutoff) || receipt.LossWindow.State != restore.LossBounded {
		t.Fatal("independent original source restore", receipt, err)
	}
	if err = data.VerifyArchivedEmpty(database.Binding, *database.Allocation, p.ArchiveID); err != nil {
		t.Fatal("restore altered archived allocation", err)
	}
	if _, err = engine.Bindings.ReadBinding(context.Background(), strings.Repeat("1", 32), remote.Epoch); err == nil {
		t.Fatal("different original binding selected")
	}
	if _, err = engine.Bindings.ReadBinding(context.Background(), remote.ID, strings.Repeat("1", 32)); err == nil {
		t.Fatal("different original epoch selected")
	}
	credentials.ExpiresAt = time.Now().UTC().Add(30 * time.Second)
	engine = h.archiveRestoreEngine(database, remote, credentials)
	engine.AllowHTTPForTests = true
	request.OperationID = "archive-expired-credential"
	if _, err = engine.Test(context.Background(), request); err == nil || gets.Load() != 1 {
		t.Fatal("insufficient credential lifetime contacted provider", err)
	}
}

func TestArchiveCredentialHostHealthUsesSealedOriginalScopeWithoutActivation(t *testing.T) {
	h, p, d, executor := retainedHostFixture(t)
	ctx := context.Background()
	if err := ensurePrivateChild(h.stateRoot, "credentials"); err != nil {
		t.Fatal(err)
	}
	files := backupcredentials.Files{Root: filepath.Join(h.stateRoot, "credentials")}
	service := backupcredentials.ArchiveService{Requester: p.Requester, Journal: h.service.Store, Files: files, Scope: h.archiveCredentialScope, Lock: func(ctx context.Context) (func() error, error) {
		lock, err := h.service.Store.AcquireHostLock(ctx)
		if err != nil {
			return nil, err
		}
		return lock.Release, nil
	}}
	binding := sqliteArchiveDatabase(t, p.Candidate.Databases[0]).Epochs[0]
	// Force the metadata-only observer's initialized branch without claiming this
	// manually journaled never-written fixture is initialized deployment evidence.
	observed := p
	observed.Candidate.Databases = append([]archive.Database(nil), p.Candidate.Databases...)
	changed := sqliteArchiveDatabase(t, observed.Candidate.Databases[0])
	changed.Written = true
	observed.Candidate.Databases[0] = archive.SQLiteDatabaseVariant(changed)
	health, err := h.ArchiveCredentialHealth(ctx, observed)
	if err != nil || len(health) != 1 || !health[0].Missing || !health[0].RenewalNeeded {
		t.Fatal("missing original credentials hidden", health, err)
	}
	packet, err := backupcredentials.ReadPacket(strings.NewReader(`{"access_key_id":"PLANTED_ACCESS","secret_access_key":"PLANTED_SECRET"}`))
	if err != nil {
		t.Fatal(err)
	}
	defer packet.Clear()
	plan, err := service.Plan(ctx, string(p.ArchiveID), string(binding.BindingID), string(binding.EpochID), nil)
	if err != nil {
		t.Fatal(err)
	}
	receipt, err := service.DeliverScoped(ctx, string(p.ArchiveID), string(binding.BindingID), string(binding.EpochID), plan.ID, packet)
	if err != nil {
		t.Fatal(err)
	}
	health, err = h.ArchiveCredentialHealth(ctx, observed)
	if err != nil || len(health) != 1 || health[0].Missing || health[0].Expired || health[0].RenewalNeeded || !health[0].ReceivedAt.Equal(receipt.ReceivedAt) {
		t.Fatal("sealed original archive health", health, err)
	}
	raw, err := json.Marshal(health)
	if err != nil || strings.Contains(string(raw), "PLANTED_") {
		t.Fatal("secret entered health metadata")
	}
	credentials, until, err := h.retainedCredentials(ctx, p.ArchiveID, binding, d.RetainUntil)
	if err != nil || !until.Equal(d.RetainUntil) || !credentials.ExpiresAt.Equal(d.RetainUntil) || credentials.AccessKey != "PLANTED_ACCESS" || credentials.SecretKey != "PLANTED_SECRET" {
		t.Fatal("exact sealed archive credentials not selected", err)
	}
	credentials = restore.Credentials{}
	if executor.stopCalls != 0 {
		t.Fatal("renewal mutated replica service")
	}
	permit, err := h.service.Store.ReadReplicaPermit(ctx, binding.DatabaseID)
	if err != nil || permit.FenceState != "held" {
		t.Fatal("renewal cleared original fence", err)
	}
	path := filepath.Join(files.Root, receipt.File)
	if err = os.Rename(path, path+"-missing"); err != nil {
		t.Fatal(err)
	}
	health, err = h.ArchiveCredentialHealth(ctx, observed)
	if err != nil || len(health) != 1 || !health[0].Missing || !health[0].RenewalNeeded {
		t.Fatal("missing sealed environment reported healthy", health, err)
	}
	if _, _, err = h.retainedCredentials(ctx, p.ArchiveID, binding, d.RetainUntil); err == nil {
		t.Fatal("missing sealed environment selected")
	}
}
