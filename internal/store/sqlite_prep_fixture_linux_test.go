//go:build linux

package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/ops"
	"github.com/ShaulLavo/brine/internal/plan"
	"github.com/ShaulLavo/brine/internal/replication"
	"github.com/ShaulLavo/brine/internal/restore"
	"github.com/ShaulLavo/brine/internal/sqliteprep"
	"github.com/ShaulLavo/brine/internal/target"
)

var errPrepFault = errors.New("injected SQLite preparation fault")

const prepRequester = "deploy:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

// prepFixture is a real control store holding a committed SQLite replica,
// current release declarations, retention evidence, an exact credential
// delivery and allocation evidence. Host effects are fake; store joins are real.
type prepFixture struct {
	t        *testing.T
	s        *Store
	app      string
	reserved ReservedDatabase
	location sqliteprep.Location
	bundle   sqliteprep.StoppedBundle
	keys     int
}

func newPrepFixture(t *testing.T) *prepFixture {
	t.Helper()
	ctx := context.Background()
	fx := &prepFixture{t: t, s: openTest(t)}
	in := fixture(t)
	req := dataRequest()
	fx.app, req.App, req.PolicyHash = string(in.Desired.Name), string(in.Desired.Name), in.Desired.PolicyHash
	var err error
	if fx.reserved, err = fx.s.ReserveDatabase(ctx, req); err != nil {
		t.Fatal(err)
	}
	fx.commitReplica()
	catalog := strings.Repeat("d", 64)
	if err = fx.s.RegisterSchemaDefinitions(ctx, fx.reserved.Database.IncarnationID, []data.SQLiteSchemaDefinition{{Database: "main", Marker: "v1", CatalogSHA256: catalog}}); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	verification, _ := data.NewID()
	in.Desired.Runtime = &data.RuntimeIdentity{UID: 10001, GID: 10001}
	in.Desired.Databases = []data.SQLiteDatabase{req.Database}
	in.Desired.SchemaCompatibility = []data.SQLiteSchemaCompatibility{{Database: "main", Startup: "preserve", Accepts: []string{"v1"}}}
	in.Desired.SchemaDefinitions = []data.SQLiteSchemaDefinition{{Database: "main", Marker: "v1", CatalogSHA256: catalog}}
	in.Desired.BackupRetention = []data.RetentionEvidence{{Destination: "primary", Endpoint: req.Destination.Endpoint, Bucket: req.Destination.Bucket, BasePrefix: req.Destination.BasePrefix, VerificationID: verification, VerifiedAt: now.Add(-time.Hour).Format(time.RFC3339Nano), FreshnessSeconds: 86400, NoObjectExpiration: true}}
	fx.commitRelease(in, "prep-release")
	fx.recordCredential(now.Add(2 * time.Hour))
	allocation := data.AllocationReceipt{DatabaseID: fx.reserved.Database.DatabaseID, IncarnationID: fx.reserved.Database.IncarnationID, RootDevice: 7, RootInode: 8, DirectoryDevice: 7, DirectoryInode: 9, AllocatedAt: now}
	if err = fx.s.RecordAllocation(ctx, allocation); err != nil {
		t.Fatal(err)
	}
	if err = os.Mkdir(filepath.Join(fx.s.dir, "replica-locks"), 0700); err != nil {
		t.Fatal(err)
	}
	fx.location = sqliteprep.Location{Device: 7, Inode: 9, MountID: 11, UID: uint32(os.Geteuid()), GID: uint32(os.Getegid()), Mode: 0700} //nolint:gosec // Test runner IDs.
	member := sqliteprep.Member{Presence: sqliteprep.Regular, Device: 7, Inode: 21, MountID: 11, UID: fx.location.UID, GID: fx.location.GID, Mode: 0600, Links: 1, Size: 8192, MtimeNS: 100, ProofSHA256: strings.Repeat("e", 64)}
	wal := member
	wal.Inode, wal.Size, wal.ProofSHA256 = 22, 4096, strings.Repeat("f", 64)
	fx.bundle = sqliteprep.StoppedBundle{Location: fx.location, DB: member, WAL: wal, SHM: sqliteprep.Member{Presence: sqliteprep.Absent}, Journal: sqliteprep.Member{Presence: sqliteprep.Absent}}
	return fx
}

func (fx *prepFixture) commitReplica() {
	binding := fx.reserved.Replica
	binding.ConfigContent = "dbs: []\n"
	sum := sha256.Sum256([]byte(binding.ConfigContent))
	binding.ConfigSHA256 = hex.EncodeToString(sum[:])
	binding.ConfigFile = filepath.Join(fx.s.dir, "replication", string(binding.BindingID), "configs", binding.ConfigSHA256+".yml")
	binding.SocketFile = filepath.Join(fx.s.dir, "replication", string(binding.BindingID), "control.sock")
	binding.LifetimeLockFile = filepath.Join(fx.s.dir, "replica-locks", string(binding.BindingID)+".lock")
	binding.UnitSHA256 = strings.Repeat("c", 64)
	binding.CredentialVersion = 1
	binding.CredentialFile = filepath.Join(fx.s.dir, "credentials/s3/primary/v1.env")
	if err := fx.s.CommitReplicaBinding(context.Background(), binding); err != nil {
		fx.t.Fatal(err)
	}
}

func (fx *prepFixture) commitRelease(in plan.Input, id string) {
	ctx := context.Background()
	p, err := plan.Build(in)
	if err != nil {
		fx.t.Fatal(err)
	}
	planID, err := fx.s.SavePlan(ctx, p, in.Desired)
	if err != nil {
		fx.t.Fatal(err)
	}
	release := Release{ID: id, PlanID: planID, Image: p.Image, HostPort: p.HostPort, Secrets: p.Secrets, Units: []target.Unit{{Name: fx.app + ".container", Hash: "sha256:" + strings.Repeat("b", 64)}}, CaddyFile: target.CaddyFile{Name: fx.app + ".caddy", Hash: "sha256:" + strings.Repeat("c", 64)}}
	if err = fx.s.CommitRelease(ctx, fx.app, release); err != nil {
		fx.t.Fatal(err)
	}
}

func (fx *prepFixture) recordCredential(expires time.Time) {
	id, _ := data.NewID()
	r := CredentialRecord{App: fx.app, CredentialRef: "primary", ID: id, Kind: "receipt", PlanID: "credential-plan", Requester: "authenticated-agent", PlanHash: "sha256:" + strings.Repeat("a", 64), TargetHash: "sha256:" + strings.Repeat("b", 64), PolicyHash: "sha256:" + strings.Repeat("a", 64), BindingID: fx.reserved.Replica.BindingID, Destination: "primary", EpochID: fx.reserved.Replica.EpochID, Version: 1, ReceivedAt: time.Now().UTC().Add(-time.Minute), ExpiresAt: &expires}
	if err := fx.s.SaveCredentialRecord(context.Background(), r); err != nil {
		fx.t.Fatal(err)
	}
}

func (fx *prepFixture) observeLocation(context.Context, data.SQLiteDatabaseBinding) (sqliteprep.Location, error) {
	return fx.location, nil
}

// operation saves a canonical input and creates its real queued task.
func (fx *prepFixture) operation(kind sqliteprep.TaskKind, preparation, supersedes string) (sqliteprep.Task, sqliteprep.Input, ops.Operation) {
	fx.t.Helper()
	ctx := context.Background()
	in := sqliteprep.Input{Kind: kind, PreparationID: preparation, App: fx.app, DatabaseID: fx.reserved.Database.DatabaseID, Supersedes: supersedes}
	ref, err := fx.s.SaveSQLiteTaskInput(ctx, in)
	if err != nil {
		fx.t.Fatal(err)
	}
	fx.keys++
	op, _, err := fx.s.CreateOperation(ctx, ops.Intent{Kind: ops.Kind(kind), App: fx.app, SecretRef: ref}, prepRequester, "prep-key-"+strings.Repeat("k", fx.keys))
	if err != nil {
		fx.t.Fatal(err)
	}
	return sqliteprep.Task{OperationID: op.ID, Kind: kind, Requester: prepRequester, App: fx.app, InputSHA256: ref}, in, op
}

// preflight creates the task and moves it to preflight as the runner would.
func (fx *prepFixture) preflight(kind sqliteprep.TaskKind, preparation, supersedes string) (sqliteprep.Task, sqliteprep.Input) {
	task, in, op := fx.operation(kind, preparation, supersedes)
	if err := fx.s.TransitionOperation(context.Background(), op.ID, ops.Queued, ops.Preflight); err != nil {
		fx.t.Fatal(err)
	}
	return task, in
}

func (fx *prepFixture) service(host *prepHost, journal sqliteprep.Journal) sqliteprep.Service {
	if journal == nil {
		journal = fx.s.SQLitePreparationJournal(fx.observeLocation)
	}
	return sqliteprep.Service{Journal: journal, Host: host}
}

func (fx *prepFixture) state(id string) ops.State {
	op, err := fx.s.GetOperation(context.Background(), id)
	if err != nil {
		fx.t.Fatal(err)
	}
	return op.State
}

func (fx *prepFixture) count(query string, args ...any) int {
	var n int
	if err := fx.s.db.QueryRowContext(context.Background(), query, args...).Scan(&n); err != nil {
		fx.t.Fatal(err)
	}
	return n
}

func (fx *prepFixture) preparation(id string) sqlitePreparation {
	p, err := readSQLitePreparation(context.Background(), fx.s.db, id)
	if err != nil {
		fx.t.Fatal(err)
	}
	return p
}

func prepID(c string) string { return strings.Repeat(c, 32) }

// prepHost is a fake host whose guards and holds are the real store-minted
// capabilities and real replica lifetime flocks. No start port exists.
type prepHost struct {
	fx         *prepFixture
	replica    sqliteprep.ReplicaState
	writers    bool
	supervisor *replication.LifetimeLock
	bundle     sqliteprep.StoppedBundle
	calls      []string
	fail       string
	hide       bool
	unowned    bool
	// onCall observes durable state immediately before a named effect.
	onCall func(string)
}

func newPrepHost(fx *prepFixture, running bool) *prepHost {
	h := &prepHost{fx: fx, replica: sqliteprep.Stopped, bundle: fx.bundle}
	if running {
		h.startSupervisor()
	}
	return h
}

func (h *prepHost) startSupervisor() {
	lock, err := replication.AcquireLifetimeLock(context.Background(), filepath.Join(h.fx.s.dir, "replica-locks", string(h.fx.reserved.Replica.BindingID)+".lock"))
	if err != nil {
		h.fx.t.Fatal(err)
	}
	h.supervisor, h.replica = lock, sqliteprep.Running
	h.fx.t.Cleanup(func() { _ = lock.Release() })
}

func (h *prepHost) call(name string) error {
	if h.onCall != nil {
		h.onCall(name)
	}
	h.calls = append(h.calls, name)
	if h.fail == name {
		return errPrepFault
	}
	return nil
}

func (h *prepHost) called(name string) bool {
	for _, c := range h.calls {
		if c == name {
			return true
		}
	}
	return false
}

func (h *prepHost) Acquire(ctx context.Context, _ sqliteprep.Input, scope sqliteprep.Scope, _ data.QuiescenceFence) (sqliteprep.HostGuard, error) {
	if err := h.call("acquire"); err != nil {
		return nil, err
	}
	g, err := h.fx.s.AcquireSQLiteMutationGuard(ctx, scope.DatabaseID)
	if err != nil {
		return nil, err
	}
	guard := &prepGuard{host: h, guard: g, scope: scope}
	if h.hide {
		return hiddenGuard{guard}, nil
	}
	return guard, nil
}

type prepGuard struct {
	host  *prepHost
	guard *SQLiteMutationGuard
	scope sqliteprep.Scope
}

// hiddenGuard is structurally a host guard but carries no store capability.
type hiddenGuard struct{ inner *prepGuard }

func (g hiddenGuard) Observe(ctx context.Context) (sqliteprep.Observation, error) {
	return g.inner.Observe(ctx)
}
func (g hiddenGuard) StopWriters(ctx context.Context, i sqliteprep.Intent) error {
	return g.inner.StopWriters(ctx, i)
}
func (g hiddenGuard) Barrier(ctx context.Context, i sqliteprep.Intent, at time.Time) (sqliteprep.RemoteSource, error) {
	return g.inner.Barrier(ctx, i, at)
}
func (g hiddenGuard) StopReplica(ctx context.Context, i sqliteprep.Intent) error {
	return g.inner.StopReplica(ctx, i)
}
func (g hiddenGuard) HoldStopped(ctx context.Context) (sqliteprep.StoppedSource, error) {
	return g.inner.HoldStopped(ctx)
}
func (g hiddenGuard) Capture(ctx context.Context, i sqliteprep.Intent, s sqliteprep.StoppedSource, at time.Time) (sqliteprep.SnapshotPlan, error) {
	return g.inner.Capture(ctx, i, s, at)
}
func (g hiddenGuard) Upload(ctx context.Context, i sqliteprep.Intent, p sqliteprep.SnapshotPlan) (sqliteprep.RemoteSource, error) {
	return g.inner.Upload(ctx, i, p)
}
func (g hiddenGuard) Verify(ctx context.Context, i sqliteprep.Intent, s sqliteprep.RemoteSource) (restore.SQLiteReceipt, error) {
	return g.inner.Verify(ctx, i, s)
}
func (g hiddenGuard) OldObject(ctx context.Context, p sqliteprep.SnapshotPlan) (sqliteprep.ObjectState, error) {
	return g.inner.OldObject(ctx, p)
}
func (g hiddenGuard) Close() error { return g.inner.Close() }

func (g *prepGuard) SQLiteMutationGuard() *SQLiteMutationGuard { return g.guard }

func (g *prepGuard) schema() data.SQLiteSchemaObservation {
	s := g.scope.Schema
	s.ObservedAt = time.Now().UTC()
	return s
}

func (g *prepGuard) Observe(context.Context) (sqliteprep.Observation, error) {
	o := sqliteprep.Observation{Replica: g.host.replica, WritersStopped: g.host.writers, JobsAbsent: true, Owned: !g.host.unowned, Schema: g.schema()}
	return o, g.host.call("observe")
}

func (g *prepGuard) StopWriters(context.Context, sqliteprep.Intent) error {
	g.host.writers = true
	return g.host.call("stop_writers")
}

func (g *prepGuard) Barrier(_ context.Context, i sqliteprep.Intent, cutoff time.Time) (sqliteprep.RemoteSource, error) {
	now := time.Now().UTC()
	point := data.SQLiteRestorePoint{ID: i.Input.PreparationID, BindingID: i.Scope.BindingID, EpochID: i.Scope.EpochID, Kind: data.RestorePointLTX, Schema: i.Scope.Schema, LTX: &data.RestoreLTXPoint{TXID: 20, Barrier: data.RestoreUploadBarrier{BindingID: i.Scope.BindingID, EpochID: i.Scope.EpochID, TXID: 20, ReplicaTXID: 21, ObservedAt: now}}, RecordedAt: now}
	return sqliteprep.RemoteSource{Origin: i.Input.PreparationID, Point: point, Cutoff: cutoff, UploadedAt: now}, g.host.call("barrier")
}

func (g *prepGuard) StopReplica(context.Context, sqliteprep.Intent) error {
	if err := g.host.call("stop_replica"); err != nil {
		return err
	}
	g.host.replica = sqliteprep.Stopped
	if g.host.supervisor != nil {
		return g.host.supervisor.Release()
	}
	return nil
}

func (g *prepGuard) HoldStopped(ctx context.Context) (sqliteprep.StoppedSource, error) {
	if err := g.host.call("hold"); err != nil {
		return nil, err
	}
	hold, err := g.guard.HoldSource(ctx)
	if err != nil {
		return nil, err
	}
	return &prepSource{guard: g, hold: hold}, nil
}

func (g *prepGuard) Capture(_ context.Context, i sqliteprep.Intent, _ sqliteprep.StoppedSource, cutoff time.Time) (sqliteprep.SnapshotPlan, error) {
	schema := i.Scope.Schema
	point := data.SQLiteRestorePoint{ID: i.Input.PreparationID, BindingID: i.Scope.BindingID, EpochID: i.Scope.EpochID, Kind: data.RestorePointSnapshot, Schema: schema, Snapshot: &data.RestoreSnapshotPoint{ObjectKey: sqliteprep.SnapshotKey(i.Scope, i.Input.PreparationID), SHA256: strings.Repeat("2", 64), Size: 4096}, RecordedAt: time.Now().UTC()}
	return sqliteprep.SnapshotPlan{Point: point, Cutoff: cutoff}, g.host.call("capture")
}

func (g *prepGuard) Upload(_ context.Context, i sqliteprep.Intent, p sqliteprep.SnapshotPlan) (sqliteprep.RemoteSource, error) {
	now := time.Now().UTC()
	s := sqliteprep.RemoteSource{Origin: i.Input.PreparationID, Point: p.Point, Cutoff: p.Cutoff, UploadedAt: now}
	s.Point.RecordedAt = now
	return s, g.host.call("upload")
}

func (g *prepGuard) Verify(_ context.Context, i sqliteprep.Intent, s sqliteprep.RemoteSource) (restore.SQLiteReceipt, error) {
	at := time.Now().UTC()
	source := s.RestoreSource()
	schema := s.Point.Schema
	r := restore.SQLiteReceipt{OperationID: i.Task.OperationID, Source: source, ToolVersion: restore.SnapshotToolVersion, ObservedAt: at, Schema: restore.SQLiteSchemaObservation{State: restore.SchemaState(schema.State), Marker: schema.Marker, CatalogSHA256: schema.CatalogSHA256}, IntegrityCheck: "passed", ForeignKeyCheck: "passed", InvariantCheck: "unknown", Coverage: &restore.CoverageEvidence{Source: source, CoveredThrough: s.Cutoff}, LossWindow: restore.LossWindow{State: restore.LossBounded, From: s.Cutoff, To: at, Reason: "Independent source-bound coverage proof bounds possible loss; later writes may be absent."}}
	if source.LTX != nil {
		r.ToolVersion, r.RequestedTXID, r.RecoveredTXID = restore.LitestreamVersion, source.LTX.TXID, source.LTX.TXID
		r.PositionEvidence, r.Barrier = "pinned_cli_exact_plan_and_successful_restore", source.LTX.Barrier
	}
	return r, g.host.call("verify")
}

func (g *prepGuard) OldObject(context.Context, sqliteprep.SnapshotPlan) (sqliteprep.ObjectState, error) {
	return sqliteprep.ObjectAbsent, g.host.call("old_head")
}

func (g *prepGuard) Close() error {
	_ = g.host.call("close_host")
	return g.guard.Release()
}

type prepSource struct {
	guard *prepGuard
	hold  *SQLiteSourceHold
}

func (s *prepSource) SQLiteSourceHold() *SQLiteSourceHold { return s.hold }

func (s *prepSource) Recheck(context.Context) (sqliteprep.StoppedBundle, error) {
	if s.hold.released.Load() {
		return sqliteprep.StoppedBundle{}, errPrepFault
	}
	return s.guard.host.bundle, s.guard.host.call("recheck")
}

func (s *prepSource) Schema(context.Context) (data.SQLiteSchemaObservation, error) {
	return s.guard.schema(), s.guard.host.call("schema")
}

func (s *prepSource) Close() error {
	_ = s.guard.host.call("close_source")
	return s.hold.Release()
}

// faultJournal injects faults immediately before or after a real journal
// write. "After" faults model a committed write whose reply was lost.
type faultJournal struct {
	*SQLitePreparationJournal
	before, after sqliteprep.Stage
	complete      string
	settle        bool
	claimLost     bool
	// abandon faults the stop-intent claim "before" its commit or "after" it.
	abandon string
}

func (j *faultJournal) ClaimAbandon(ctx context.Context, s sqliteprep.AbandonStop, o sqliteprep.HostObservation) error {
	if j.abandon == "before" {
		return errPrepFault
	}
	err := j.SQLitePreparationJournal.ClaimAbandon(ctx, s, o)
	if err == nil && j.abandon == "after" {
		return errPrepFault
	}
	return err
}

func (j *faultJournal) Claim(ctx context.Context, i sqliteprep.Intent, o sqliteprep.HostObservation, held sqliteprep.StoppedSource) error {
	err := j.SQLitePreparationJournal.Claim(ctx, i, o, held)
	if err == nil && j.claimLost {
		return errPrepFault
	}
	return err
}

func (j *faultJournal) Append(ctx context.Context, i sqliteprep.Intent, e sqliteprep.Event) error {
	if e.Stage == j.before {
		return errPrepFault
	}
	err := j.SQLitePreparationJournal.Append(ctx, i, e)
	if err == nil && e.Stage == j.after {
		return errPrepFault
	}
	return err
}

func (j *faultJournal) CompletePrepare(ctx context.Context, h sqliteprep.History, p sqliteprep.PreparedPoint, held sqliteprep.StoppedSource) (sqliteprep.Receipt, error) {
	if j.complete == "before" {
		return sqliteprep.Receipt{}, errPrepFault
	}
	r, err := j.SQLitePreparationJournal.CompletePrepare(ctx, h, p, held)
	if err == nil && j.complete == "after" {
		return sqliteprep.Receipt{}, errPrepFault
	}
	return r, err
}

func (j *faultJournal) CompleteAbandon(ctx context.Context, t sqliteprep.Task, in sqliteprep.Input, a sqliteprep.Abandonment, held sqliteprep.StoppedSource) (sqliteprep.Receipt, error) {
	if j.complete == "release_hold" {
		_ = sqliteHoldOf(held).Release()
	}
	if j.complete == "strip_stop" {
		a.Stop = nil
	}
	if j.complete == "cross_stop" && a.Stop != nil {
		crossed := *a.Stop
		crossed.StopWriters = !crossed.StopWriters
		a.Stop = &crossed
	}
	r, err := j.SQLitePreparationJournal.CompleteAbandon(ctx, t, in, a, held)
	if err == nil && j.complete == "after" {
		return sqliteprep.Receipt{}, errPrepFault
	}
	return r, err
}

func (j *faultJournal) MarkRecovery(ctx context.Context, t sqliteprep.Task, c sqliteprep.Code) error {
	if j.settle {
		return errPrepFault
	}
	return j.SQLitePreparationJournal.MarkRecovery(ctx, t, c)
}
