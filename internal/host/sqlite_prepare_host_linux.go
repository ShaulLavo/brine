//go:build linux

package host

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/localexec"
	"github.com/ShaulLavo/brine/internal/replication"
	"github.com/ShaulLavo/brine/internal/restore"
	"github.com/ShaulLavo/brine/internal/sqliteprep"
	"github.com/ShaulLavo/brine/internal/store"
	"github.com/ShaulLavo/brine/internal/systemd"
	"github.com/ShaulLavo/brine/internal/target"
	"golang.org/x/sys/unix"
)

const sqlitePrepareStopTimeout = 3 * time.Minute

var errSQLitePrepareHost = errors.New("host: SQLite preparation host refused")

// sqlitePreparationHost is the sqliteprep.Host of committed SQLite replicas.
// It can stop the app writer unit and the replica unit, run the replica's own
// sync barrier, capture, upload create-only and verify in isolation. It has no
// start, restart, exec-into-replica, credential activation or deletion path.
type sqlitePreparationHost struct {
	state     *store.Store
	stateRoot string
	home      string
	session   localexec.Session
	executor  localexec.Executor
	// procRoot, cli, endpoint and allowHTTP are test seams. Production reads
	// /proc, runs the pinned CLI and uses the recorded HTTPS destination.
	procRoot  string
	cli       restore.CLI
	endpoint  string
	allowHTTP bool
}

func (h sqlitePreparationHost) Acquire(ctx context.Context, scope sqliteprep.Scope, _ data.QuiescenceFence) (sqliteprep.HostGuard, error) {
	guard, err := h.state.AcquireSQLiteMutationGuard(ctx, scope.DatabaseID)
	if err != nil {
		return nil, err
	}
	g := &sqliteHostGuard{host: h, guard: guard, scope: scope, snapshots: map[string][]byte{}}
	if err = g.bind(ctx); err != nil {
		return nil, errors.Join(err, g.Close())
	}
	return g, nil
}

// sqliteHostGuard holds the store-minted host mutation guard for one task.
type sqliteHostGuard struct {
	host          sqlitePreparationHost
	guard         *store.SQLiteMutationGuard
	scope         sqliteprep.Scope
	permit        store.ReplicaPermit
	artifact      replication.Artifacts
	definitions   []data.SQLiteSchemaDefinition
	held          *sqliteSource
	snapshots     map[string][]byte
	writer        target.Unit
	writerRelease string
}

func (g *sqliteHostGuard) SQLiteMutationGuard() *store.SQLiteMutationGuard { return g.guard }

// bind proves the committed binding, the admitted persistent root, the
// replica unit artifact on disk and the exact delivered credential version.
func (g *sqliteHostGuard) bind(ctx context.Context) error {
	s := g.scope
	permit, err := g.host.state.ReadReplicaPermit(ctx, s.DatabaseID)
	if err != nil {
		return err
	}
	r := permit.Replica
	lock := filepath.Join(g.host.stateRoot, "replica-locks", string(r.BindingID)+".lock")
	if !r.Committed || r.BindingID != s.BindingID || r.EpochID != s.EpochID || r.ConfigSHA256 != s.ConfigSHA256 || r.CredentialVersion != s.CredentialVersion || r.LifetimeLockFile != lock || permit.Database.Name != s.DatabaseName || permit.Database.IncarnationID != s.IncarnationID {
		return errSQLitePrepareHost
	}
	if err = admitSQLiteRoot(ctx, g.host.state, permit.Database); err != nil {
		return err
	}
	artifact, hash, err := (replicaRotation{state: g.host.state, stateRoot: g.host.stateRoot, home: g.host.home}).artifact(ctx, r)
	if err != nil || hash != r.UnitSHA256 || data.VerifyRunnerFile(artifact.ServicePath) != nil {
		return errSQLitePrepareHost
	}
	installed, err := os.ReadFile(artifact.ServicePath)
	if err != nil || !bytes.Equal(installed, artifact.Service) {
		return errSQLitePrepareHost
	}
	g.permit, g.artifact = permit, artifact
	g.definitions = []data.SQLiteSchemaDefinition{}
	if s.Schema.Marker != data.EmptyMarker {
		g.definitions = append(g.definitions, data.SQLiteSchemaDefinition{Database: s.DatabaseName, Marker: s.Schema.Marker, CatalogSHA256: s.Schema.CatalogSHA256})
	}
	return g.bindWriter(ctx)
}

// admitSQLiteRoot requires the persistent root to be the allocation's measured
// root with its preparation-time proofs, as archive admission does.
func admitSQLiteRoot(ctx context.Context, state *store.Store, b data.SQLiteDatabaseBinding) error {
	allocation, err := state.ReadPreparationEvidence(ctx, b.DatabaseID)
	if err != nil || allocation == nil || allocation.RootProof == nil {
		return errSQLitePrepareHost
	}
	root, err := data.InspectRoot(string(b.Root))
	if err == nil {
		root, err = root.WithPreparationProof(allocation.RootProof)
	}
	if err != nil || !root.Admits(b.Root, 0) || root.Device != allocation.RootDevice || root.Inode != allocation.RootInode {
		return errSQLitePrepareHost
	}
	return nil
}

func (g *sqliteHostGuard) appUnit() string { return g.scope.App + ".service" }
func (g *sqliteHostGuard) replicaUnit() string {
	name, _ := replication.ServiceName(string(g.scope.BindingID))
	return name
}
func (g *sqliteHostGuard) sourceDirectory() string {
	d := g.permit.Database
	return filepath.Join(string(d.Root), d.RelativeDirectory)
}

// Observe reports actual unit, container, ownership and schema facts. A
// running replica counts only when its unit's main process in its own cgroup
// holds the replica lifetime lock; transitional states are not a known state.
func (g *sqliteHostGuard) Observe(ctx context.Context) (sqliteprep.Observation, error) {
	app, err := showUnit(ctx, g.host.session, g.appUnit())
	if err != nil {
		return sqliteprep.Observation{}, err
	}
	replica, err := showUnit(ctx, g.host.session, g.replicaUnit())
	if err != nil {
		return sqliteprep.Observation{}, err
	}
	o := sqliteprep.Observation{Replica: "unknown", JobsAbsent: app.Job == "" && replica.Job == "" && app.ControlPID == 0 && replica.ControlPID == 0}
	o.WritersStopped = app.stopped(false) && containersOutside(ctx, g.host.session, []string{g.sourceDirectory()})
	owned := g.writerOwned(ctx, app) && replica.Load == "loaded"
	switch {
	case replica.stopped(false):
		o.Replica = sqliteprep.Stopped
	case replica.Active == "active" && replica.Sub == "running":
		o.Replica = sqliteprep.Running
		owned = owned && g.supervisorHoldsLifetime(replica)
	}
	o.Owned = owned
	o.Schema, err = g.observeSchema(ctx)
	return o, err
}

func (g *sqliteHostGuard) observeSchema(ctx context.Context) (data.SQLiteSchemaObservation, error) {
	if g.held != nil {
		return g.held.Schema(ctx)
	}
	return observeSQLiteSchema(ctx, g.permit.Database, g.definitions), nil
}

// supervisorHoldsLifetime proves that the replica unit's main process holds
// the exact lifetime flock and runs in that unit's own cgroup. Lock
// unavailability alone does not identify its owner.
func (g *sqliteHostGuard) supervisorHoldsLifetime(u unitState) bool {
	if u.MainPID == 0 || u.ControlGroup == "" || !strings.HasSuffix(u.ControlGroup, "/"+g.replicaUnit()) {
		return false
	}
	var lock unix.Stat_t
	if unix.Lstat(g.permit.Replica.LifetimeLockFile, &lock) != nil || lock.Mode&unix.S_IFMT != unix.S_IFREG {
		return false
	}
	holders, err := flockHolders(filepath.Join(g.host.proc(), "locks"), uint64(lock.Dev), lock.Ino) //nolint:gosec // Device numbers are nonnegative.
	if err != nil || len(holders) != 1 || holders[0] != u.MainPID {
		return false
	}
	return g.processInUnit(u, g.replicaUnit())
}

func (g *sqliteHostGuard) processInUnit(u unitState, unit string) bool {
	if u.MainPID == 0 || u.ControlGroup == "" || !strings.HasSuffix(u.ControlGroup, "/"+unit) {
		return false
	}
	raw, err := os.ReadFile(filepath.Join(g.host.proc(), strconv.FormatUint(u.MainPID, 10), "cgroup"))
	return err == nil && len(raw) < 4096 && strings.TrimSpace(string(raw)) == "0::"+u.ControlGroup
}

func (h sqlitePreparationHost) proc() string {
	if h.procRoot != "" {
		return h.procRoot
	}
	return "/proc"
}

// flockHolders parses /proc/locks for granted FLOCK entries on one inode.
// Waiting entries ("->") are not holders.
func flockHolders(path string, device, inode uint64) ([]uint64, error) {
	file, err := os.Open(path) // #nosec G304 -- Fixed procfs path or test fixture.
	if err != nil {
		return nil, err
	}
	defer func() { _ = file.Close() }()
	wanted := fmt.Sprintf("%02x:%02x:%d", unix.Major(device), unix.Minor(device), inode)
	holders := []uint64{}
	scanner := bufio.NewScanner(file)
	for lines := 0; scanner.Scan(); lines++ {
		fields := strings.Fields(scanner.Text())
		if lines > 65536 {
			return nil, errSQLitePrepareHost
		}
		if len(fields) < 6 || fields[1] == "->" || fields[1] != "FLOCK" || fields[5] != wanted {
			continue
		}
		pid, err := strconv.ParseUint(fields[4], 10, 64)
		if err != nil {
			return nil, errSQLitePrepareHost
		}
		holders = append(holders, pid)
	}
	return holders, scanner.Err()
}

// stopUnit stops one recorded unit and proves it stopped. It never starts,
// restarts or reloads anything.
func (g *sqliteHostGuard) stopUnit(ctx context.Context, name string) error {
	unit, err := showUnit(ctx, g.host.session, name)
	if err != nil || unit.Load != "loaded" {
		return errSQLitePrepareHost
	}
	if unit.stopped(false) {
		return nil
	}
	session, err := g.host.session.WithTimeout(sqlitePrepareStopTimeout)
	if err != nil {
		return err
	}
	r, err := session.Execute(ctx, "/usr/bin/systemctl", []string{"--user", "stop", name}, nil, true)
	if err != nil || r.ExitCode != 0 || r.Truncated {
		return errors.Join(errSQLitePrepareHost, err)
	}
	unit, err = showUnit(ctx, g.host.session, name)
	if err != nil || !unit.stopped(false) {
		return errors.Join(errSQLitePrepareHost, err)
	}
	return nil
}

func (g *sqliteHostGuard) StopWriters(ctx context.Context, _ sqliteprep.Intent) error {
	writer, err := showUnit(ctx, g.host.session, g.appUnit())
	if err != nil || !g.writerOwned(ctx, writer) {
		return errSQLitePrepareHost
	}
	if err := g.stopUnit(ctx, g.appUnit()); err != nil {
		return err
	}
	if !containersOutside(ctx, g.host.session, []string{g.sourceDirectory()}) {
		return errSQLitePrepareHost
	}
	return nil
}

func (g *sqliteHostGuard) StopReplica(ctx context.Context, _ sqliteprep.Intent) error {
	unit, err := systemd.ParseUnit(g.replicaUnit())
	if err != nil || verifyEngineLoadedService(ctx, g.host.session, g.host.home, unit) != nil {
		return errSQLitePrepareHost
	}
	observed, err := g.Observe(ctx)
	if err != nil || !observed.Owned || !observed.JobsAbsent {
		return errSQLitePrepareHost
	}
	return g.stopUnit(ctx, g.replicaUnit())
}

// Barrier asks the running replica, through its own control socket, to upload
// through the current position. Writers already stopped at the cutoff.
func (g *sqliteHostGuard) Barrier(ctx context.Context, i sqliteprep.Intent, cutoff time.Time) (sqliteprep.RemoteSource, error) {
	if g.host.executor == nil {
		return sqliteprep.RemoteSource{}, errSQLitePrepareHost
	}
	barrier, err := replication.SyncBarrier(ctx, g.host.executor, g.artifact.Binding, 2*time.Minute)
	if err != nil {
		return sqliteprep.RemoteSource{}, err
	}
	s := g.scope
	at := barrier.ObservedAt.UTC()
	if barrier.BindingID != string(s.BindingID) || barrier.EpochID != string(s.EpochID) || barrier.DatabaseID != string(s.DatabaseID) || at.Before(cutoff) {
		return sqliteprep.RemoteSource{}, errSQLitePrepareHost
	}
	point := data.SQLiteRestorePoint{ID: i.Input.PreparationID, BindingID: s.BindingID, EpochID: s.EpochID, Kind: data.RestorePointLTX, Schema: s.Schema, LTX: &data.RestoreLTXPoint{TXID: barrier.TXID, Barrier: data.RestoreUploadBarrier{BindingID: s.BindingID, EpochID: s.EpochID, TXID: barrier.TXID, ReplicaTXID: barrier.ReplicaTXID, ObservedAt: at}}, RecordedAt: time.Now().UTC()}
	source := sqliteprep.RemoteSource{Origin: i.Input.PreparationID, Point: point, Cutoff: cutoff, UploadedAt: at}
	return source, source.Validate()
}

// HoldStopped takes the replica lifetime lock beneath the mutation guard and
// retains the source handles. A running supervisor makes the lock unavailable.
func (g *sqliteHostGuard) HoldStopped(ctx context.Context) (sqliteprep.StoppedSource, error) {
	if g.held != nil {
		return nil, errSQLitePrepareHost
	}
	hold, err := g.guard.HoldSource(ctx)
	if err != nil {
		return nil, err
	}
	source, err := openSQLiteSource(g.permit.Database, g.definitions, hold)
	if err != nil {
		return nil, errors.Join(err, hold.Release())
	}
	g.held = source
	return source, nil
}

// Capture copies the held stopped source and proves the authoritative bundle
// was identical before and after. The bytes stay in this guard for the one
// upload invocation and are cleared on Close.
func (g *sqliteHostGuard) Capture(ctx context.Context, i sqliteprep.Intent, held sqliteprep.StoppedSource, cutoff time.Time) (sqliteprep.SnapshotPlan, error) {
	if held == nil || held != sqliteprep.StoppedSource(g.held) {
		return sqliteprep.SnapshotPlan{}, errSQLitePrepareHost
	}
	before, err := held.Recheck(ctx)
	if err != nil {
		return sqliteprep.SnapshotPlan{}, err
	}
	id := i.Input.PreparationID
	raw, err := captureStoppedSQLite(ctx, g.host.stateRoot, "sqlite-prepare-snapshots", g.permit.Database, id)
	if err != nil {
		return sqliteprep.SnapshotPlan{}, err
	}
	after, err := held.Recheck(ctx)
	if err != nil || !before.Matches(after) || len(raw) > sqliteprep.MaxCaptureBytes {
		clear(raw)
		return sqliteprep.SnapshotPlan{}, errors.Join(errSQLiteSource, err)
	}
	sum := sha256.Sum256(raw)
	s := g.scope
	point := data.SQLiteRestorePoint{ID: id, BindingID: s.BindingID, EpochID: s.EpochID, Kind: data.RestorePointSnapshot, Schema: sqlitePointSchema(s.Schema), Snapshot: &data.RestoreSnapshotPoint{ObjectKey: sqliteprep.SnapshotKey(s, id), SHA256: hex.EncodeToString(sum[:]), Size: int64(len(raw))}, RecordedAt: time.Now().UTC()}
	g.snapshots[id] = raw
	return sqliteprep.SnapshotPlan{Point: point, Cutoff: cutoff}, nil
}

func sqlitePointSchema(s data.SQLiteSchemaObservation) data.SQLiteSchemaObservation {
	if s.State == data.AllocatedEmpty {
		s.State = data.VerifiedEmpty
	}
	return s
}

func (g *sqliteHostGuard) binding() restore.Binding {
	return withTestEndpoint(restoreBinding(g.permit.Replica), g.host.endpoint)
}

// withTestEndpoint redirects a binding to a local fake only in tests.
func withTestEndpoint(b restore.Binding, endpoint string) restore.Binding {
	if endpoint != "" {
		b.Destination.Endpoint, b.Destination.PathStyle = endpoint, true
	}
	return b
}

// credentials reads exactly the scope's delivered version; never the newest.
func (g *sqliteHostGuard) credentials(ctx context.Context) (restore.Credentials, error) {
	r := g.permit.Replica
	receipt, err := g.host.state.CredentialReceipt(ctx, r.BindingID, g.scope.CredentialVersion)
	if err != nil || receipt.App != g.scope.App || receipt.Version != g.scope.CredentialVersion || receipt.EpochID != r.EpochID || receipt.CredentialRef != r.Destination.CredentialRef || receipt.Destination != r.Destination.Reference {
		return restore.Credentials{}, errors.Join(errSQLitePrepareHost, err)
	}
	return readDeliveredCredentials(g.host.stateRoot, receipt)
}

// Upload makes the shipped single create-only invocation: one conditional
// create and its expected 412 capability check. Its bytes are cleared after.
func (g *sqliteHostGuard) Upload(ctx context.Context, i sqliteprep.Intent, p sqliteprep.SnapshotPlan) (sqliteprep.RemoteSource, error) {
	id := i.Input.PreparationID
	raw := g.snapshots[id]
	delete(g.snapshots, id)
	defer clear(raw)
	if raw == nil || p.Point.ID != id || p.Point.Snapshot == nil {
		return sqliteprep.RemoteSource{}, errSQLitePrepareHost
	}
	credentials, err := g.credentials(ctx)
	if err != nil {
		return sqliteprep.RemoteSource{}, err
	}
	defer func() { credentials = restore.Credentials{} }()
	uploaded, at, err := restore.UploadSnapshot(ctx, g.binding(), credentials, id, raw, 3*time.Minute, g.host.allowHTTP)
	if err != nil {
		return sqliteprep.RemoteSource{}, err
	}
	planned := p.Point.Snapshot
	if uploaded.ObjectKey != planned.ObjectKey || uploaded.SHA256 != planned.SHA256 || uploaded.Size != planned.Size {
		return sqliteprep.RemoteSource{}, errSQLitePrepareHost
	}
	point := p.Point
	point.RecordedAt = time.Now().UTC()
	source := sqliteprep.RemoteSource{Origin: id, Point: point, Cutoff: p.Cutoff, UploadedAt: at.UTC()}
	return source, source.Validate()
}

// Verify restores the exact source in a private workspace and returns
// metadata only after the workspace is disposed.
func (g *sqliteHostGuard) Verify(ctx context.Context, i sqliteprep.Intent, s sqliteprep.RemoteSource) (restore.SQLiteReceipt, error) {
	credentials, err := g.credentials(ctx)
	if err != nil {
		return restore.SQLiteReceipt{}, err
	}
	defer func() { credentials = restore.Credentials{} }()
	request := sqliteSourceRequest(i.Task.OperationID, s.Point, s.Cutoff, g.scope.Checks)
	engine := pinnedRestoreEngine(filepath.Join(g.host.stateRoot, "sqlite-prepare-restores"), g.scope.DatabaseName, g.definitions, g.binding(), credentials, errSQLitePrepareHost)
	engine.CLI, engine.AllowHTTPForTests = g.host.cli, g.host.allowHTTP
	if err = ensurePrivateChild(g.host.stateRoot, "sqlite-prepare-restores"); err != nil {
		return restore.SQLiteReceipt{}, err
	}
	return checkSQLiteRestore(ctx, &engine, request)
}

// sqliteSourceRequest restores exactly one recorded point, bound to its
// independent writer cutoff and the retained declared invariants.
func sqliteSourceRequest(operation string, point data.SQLiteRestorePoint, cutoff time.Time, checks []data.RestoreInvariant) restore.SQLiteRequest {
	request := requestAtPoint(restore.SQLiteRequest{OperationID: operation, Budget: 3 * time.Minute}, point)
	request.Coverage = &restore.CoverageEvidence{Source: request.Source, CoveredThrough: cutoff}
	for _, c := range checks {
		request.Invariants = append(request.Invariants, restore.Invariant{Kind: restore.InvariantKind(c.Kind), Table: c.Table, Column: c.Column, Count: c.Count, Minimum: c.Minimum, Maximum: c.Maximum})
	}
	return request
}

// OldObject is informational and optional. No read-only object probe is
// shipped, so it reports unknown rather than adding provider authority.
func (g *sqliteHostGuard) OldObject(context.Context, sqliteprep.SnapshotPlan) (sqliteprep.ObjectState, error) {
	return sqliteprep.ObjectUnknown, nil
}

// Close clears retained snapshot bytes and releases the mutation guard. The
// service closes the held source first, after any completion transaction.
func (g *sqliteHostGuard) Close() error {
	for id, raw := range g.snapshots {
		clear(raw)
		delete(g.snapshots, id)
	}
	return g.guard.Release()
}
