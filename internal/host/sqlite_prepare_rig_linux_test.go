//go:build linux

package host

import (
	"context"
	"database/sql"
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
	"testing"
	"time"

	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/dispatch"
	"github.com/ShaulLavo/brine/internal/jobs"
	"github.com/ShaulLavo/brine/internal/localexec"
	"github.com/ShaulLavo/brine/internal/ops"
	"github.com/ShaulLavo/brine/internal/plan"
	"github.com/ShaulLavo/brine/internal/policy"
	"github.com/ShaulLavo/brine/internal/quadlet"
	"github.com/ShaulLavo/brine/internal/replicapermits"
	"github.com/ShaulLavo/brine/internal/replication"
	"github.com/ShaulLavo/brine/internal/spec"
	"github.com/ShaulLavo/brine/internal/sqliteprep"
	"github.com/ShaulLavo/brine/internal/store"
	"github.com/ShaulLavo/brine/internal/systemd"
	"github.com/ShaulLavo/brine/internal/target"
	"golang.org/x/sys/unix"
)

const sqlitePrepRequester = "deploy:" + "abababababababababababababababababababababababababababababababab"
const sqlitePrepPID = 4242
const sqlitePrepWriterPID = 4243

var sqlitePrepSchemas = map[string]string{
	"v1": "CREATE TABLE items(id INTEGER PRIMARY KEY, name TEXT NOT NULL)",
	"v2": "CREATE TABLE items(id INTEGER PRIMARY KEY, name TEXT NOT NULL); CREATE TABLE notes(id INTEGER PRIMARY KEY, body TEXT)",
	"v3": "CREATE TABLE items(id INTEGER PRIMARY KEY, name TEXT NOT NULL); CREATE TABLE other(id INTEGER PRIMARY KEY)",
}

// sqlitePrepRig is a real control store with a committed replica, its real
// config and unit artifacts, a real persistent root and SQLite file, an exact
// delivered credential and a committed release. systemctl, Podman, the
// Litestream socket, /proc and object storage are local fakes. Nothing in
// this rig can start a unit: unexpected commands fail and are recorded.
type sqlitePrepRig struct {
	t          *testing.T
	state      *store.Store
	root       string
	home       string
	database   data.SQLiteDatabaseBinding
	replica    data.ReplicaBinding
	exec       *sqlitePrepExecutor
	remote     *sqlitePrepRemote
	proc       string
	supervisor *replication.LifetimeLock
	keys       int
	source     []byte
}

func sqlitePrepCatalog(t *testing.T, ddl string) string {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "catalog.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	if _, err = db.Exec(ddl); err != nil {
		t.Fatal(err)
	}
	rows, err := db.Query("SELECT type,name,tbl_name,sql FROM sqlite_schema WHERE name NOT LIKE 'sqlite_%'")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	catalog := []data.CatalogRow{}
	for rows.Next() {
		var r data.CatalogRow
		var text sql.NullString
		if err = rows.Scan(&r.Type, &r.Name, &r.TableName, &text); err != nil {
			t.Fatal(err)
		}
		if text.Valid {
			r.SQL = &text.String
		}
		catalog = append(catalog, r)
	}
	_, hash, err := data.CatalogFingerprint(catalog)
	if err != nil {
		t.Fatal(err)
	}
	return hash
}

// writeSQLitePrepDatabase replaces the database file with a marked schema.
func writeSQLitePrepDatabase(t *testing.T, path, marker string) {
	t.Helper()
	_ = os.Remove(path)
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	for _, statement := range []string{sqlitePrepSchemas[marker], data.MarkerTableSQL, "INSERT INTO items(name) VALUES ('committed-row')"} {
		if _, err = db.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = db.Exec("INSERT INTO brine_schema_marker VALUES (1, ?, ?)", marker, sqlitePrepCatalog(t, sqlitePrepSchemas[marker])); err != nil {
		t.Fatal(err)
	}
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
	if err = os.Chmod(path, 0600); err != nil {
		t.Fatal(err)
	}
}

func newSQLitePrepRig(t *testing.T, accepts []string, marker string) *sqlitePrepRig {
	t.Helper()
	ctx := context.Background()
	_, desired, facts := persistentHostFixture(t)
	desired.ContainerPort = 3000
	desired.Domains = []spec.Domain{"hello.example.com"}
	desired.Health = policy.Health{Path: "/", ExpectedStatus: 200, StartupDeadlineSeconds: 30, TimeoutSeconds: 3}
	desired.Resources = policy.Resources{MemoryMB: 512, PIDsLimit: 128}
	desired.Environment = []policy.Environment{}
	desired.Secrets = []policy.Secret{}
	desired.SchemaCompatibility = []data.SQLiteSchemaCompatibility{{Database: "main", Startup: "preserve", Accepts: accepts}}
	desired.SchemaDefinitions = nil
	for _, m := range []string{"v1", "v2"} {
		desired.SchemaDefinitions = append(desired.SchemaDefinitions, data.SQLiteSchemaDefinition{Database: "main", Marker: m, CatalogSHA256: sqlitePrepCatalog(t, sqlitePrepSchemas[m])})
	}
	r := &sqlitePrepRig{t: t, root: shortReplicaStateRoot(t), proc: t.TempDir()}
	var err error
	if r.state, err = store.Open(r.root); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.state.Close() })
	facts.Store, facts.StateRoot = r.state, r.root
	r.database = collectPersistent(t, facts, desired).Database
	writeSQLitePrepDatabase(t, r.path(""), marker)
	record := r.deliverCredential(desired)
	home := t.TempDir()
	if err = os.Chmod(home, 0700); err != nil { //nolint:gosec // Private directory requires traversal permission.
		t.Fatal(err)
	}
	r.home = home
	manager := &systemd.Fake{DaemonReloadFunc: func(context.Context) error { return nil }, JobPendingFunc: func(context.Context, systemd.Unit) (bool, error) { return false, nil }, ShowFunc: func(context.Context, systemd.Unit) (systemd.Properties, error) {
		return systemd.Properties{ActiveState: "active", SubState: "running"}, nil
	}}
	permits := replicapermits.StorePermits{State: r.state, Configs: replication.DiskConfigs{}}
	preparation := DataPreparation{Runner: &preparationPullRunner{}, ProbeRoot: facts.ProbeRoot, ProbeMapping: facts.ProbeMapping, State: r.state, StateRoot: r.root, Home: home, Permits: permits, Publisher: replication.ArtifactPublisher{StateRoot: r.root, UnitRoot: filepath.Join(home, ".config/systemd/user")}, Services: &preparedServices{}, Units: manager}
	planned := plan.Plan{DataCredentials: []data.CredentialEvidence{{BindingID: record.BindingID, EpochID: record.EpochID, Destination: record.Destination, Reference: record.CredentialRef, Version: record.Version, PolicyHash: record.PolicyHash, ReceivedAt: record.ReceivedAt, ExpiresAt: record.ExpiresAt}}, DataMounts: []data.Mount{{Database: r.database, HostPath: r.path("-dir"), ContainerPath: r.database.MountPath, BindingID: r.database.ReplicaBindingID}}}
	if err = preparation.PreparePersistent(ctx, "fixture", planned, desired); err != nil {
		t.Fatal("replica commit", err)
	}
	permit, err := r.state.ReadReplicaPermit(ctx, r.database.DatabaseID)
	if err != nil || !permit.Replica.Committed {
		t.Fatal("replica not committed", err)
	}
	r.replica = permit.Replica
	r.commitRelease(desired, facts)
	// #nosec G304 -- Exact private fixture database path.
	if r.source, err = os.ReadFile(r.path("")); err != nil {
		t.Fatal(err)
	}
	r.exec = &sqlitePrepExecutor{home: r.home, units: map[string]string{"hello.service": "running", r.replicaUnit(): "stopped"}, dbPath: r.path("")}
	r.writeProc(fmt.Sprintf("%d/cgroup", sqlitePrepWriterPID), "0::/user.slice/app.slice/hello.service\n")
	r.remote = newSQLitePrepRemote(t)
	return r
}

// path returns the database file plus suffix; "-dir" is its directory.
func (r *sqlitePrepRig) path(suffix string) string {
	dir := filepath.Join(string(r.database.Root), r.database.RelativeDirectory)
	if suffix == "-dir" {
		return dir
	}
	return filepath.Join(dir, string(r.database.Filename)) + suffix
}

func (r *sqlitePrepRig) replicaUnit() string {
	name, _ := replication.ServiceName(string(r.database.ReplicaBindingID))
	return name
}

func (r *sqlitePrepRig) deliverCredential(d policy.Desired) store.CredentialRecord {
	r.t.Helper()
	permit, err := r.state.ReadReplicaPermit(context.Background(), r.database.DatabaseID)
	if err != nil {
		r.t.Fatal(err)
	}
	if err = ensurePrivateChild(r.root, "credentials/s3/primary"); err != nil {
		r.t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(r.root, "credentials/s3/primary/v1.env"), []byte("AWS_ACCESS_KEY_ID=fixture-access\nAWS_SECRET_ACCESS_KEY=fixture-secret\n"), 0600); err != nil {
		r.t.Fatal(err)
	}
	expires := time.Now().UTC().Add(2 * time.Hour)
	record := store.CredentialRecord{ID: strings.Repeat("e", 32), Kind: "receipt", App: "hello", CredentialRef: "primary", Requester: "fixture", PlanID: "sha256:" + strings.Repeat("f", 64), PlanHash: "sha256:" + strings.Repeat("f", 64), TargetHash: "sha256:" + strings.Repeat("b", 64), PolicyHash: d.PolicyHash, BindingID: permit.Replica.BindingID, Destination: permit.Replica.Destination.Reference, EpochID: permit.Replica.EpochID, Version: 1, ReceivedAt: time.Now().UTC().Add(-time.Minute), ExpiresAt: &expires}
	if err = r.state.SaveCredentialRecord(context.Background(), record); err != nil {
		r.t.Fatal(err)
	}
	return record
}

func (r *sqlitePrepRig) commitRelease(d policy.Desired, facts DataFacts) {
	r.t.Helper()
	raw, err := os.ReadFile("../target/testdata/ready-arm64.json")
	if err != nil {
		r.t.Fatal(err)
	}
	snapshot, err := target.Decode(raw)
	if err != nil {
		r.t.Fatal(err)
	}
	persistent, err := facts.Collect(context.Background(), d)
	if err != nil || persistent.Status != target.KnownStatus {
		r.t.Fatal("persistent writer facts", err, persistent.Status)
	}
	snapshot.PersistentData = &persistent
	in := plan.Input{Desired: d, Snapshot: snapshot, Image: plan.Image{Digest: strings.Split(string(d.Image), "@")[1], Platform: target.Platform{OS: "linux", Arch: snapshot.Arch}, ManifestDigest: target.Known("sha256:" + strings.Repeat("b", 64))}, State: plan.BrineState{Target: snapshot.Identity, Generation: *snapshot.Generation.Value, Releases: []plan.CurrentRelease{}}}
	p, err := plan.Build(in)
	if err != nil {
		r.t.Fatal("plan", err)
	}
	if p.Kind == plan.Conflict {
		r.t.Fatal("writer plan conflicts", p.Conflicts)
	}
	unit, err := quadlet.Render(d, p, *p.Image.ManifestDigest.Value)
	if err != nil {
		r.t.Fatal("writer render", err)
	}
	manager, err := quadlet.NewManager(r.home, sqlitePrepValidator{})
	if err != nil {
		r.t.Fatal(err)
	}
	if err = manager.Install(context.Background(), unit, ""); err != nil {
		r.t.Fatal("writer install", err)
	}
	if err = manager.Close(); err != nil {
		r.t.Fatal(err)
	}
	id, err := r.state.SavePlan(context.Background(), p, d)
	if err != nil {
		r.t.Fatal(err)
	}
	release := store.Release{ID: "sqlite-prep-release", PlanID: id, Image: p.Image, HostPort: p.HostPort, Secrets: p.Secrets, Units: []target.Unit{{Name: unit.Name(), Hash: unit.Hash()}}, CaddyFile: target.CaddyFile{Name: "hello.caddy", Hash: "sha256:" + strings.Repeat("c", 64)}}
	if err = r.state.CommitRelease(context.Background(), "hello", release); err != nil {
		r.t.Fatal("release", err)
	}
}

// startSupervisor holds the real lifetime flock as the replica's main process
// would and publishes matching /proc/locks and cgroup facts for that PID.
func (r *sqlitePrepRig) startSupervisor() {
	r.t.Helper()
	lock, err := replication.AcquireLifetimeLock(context.Background(), r.replica.LifetimeLockFile)
	if err != nil {
		r.t.Fatal(err)
	}
	r.supervisor = lock
	r.t.Cleanup(func() { _ = lock.Release() })
	var stat unix.Stat_t
	if err = unix.Lstat(r.replica.LifetimeLockFile, &stat); err != nil {
		r.t.Fatal(err)
	}
	dev := uint64(stat.Dev) //nolint:gosec // Device numbers are nonnegative.
	locks := fmt.Sprintf("1: POSIX  ADVISORY  WRITE 99 00:01:2 0 EOF\n2: FLOCK  ADVISORY  WRITE %d %02x:%02x:%d 0 EOF\n", sqlitePrepPID, unix.Major(dev), unix.Minor(dev), stat.Ino)
	r.writeProc("locks", locks)
	r.writeProc(fmt.Sprintf("%d/cgroup", sqlitePrepPID), "0::/user.slice/app.slice/"+r.replicaUnit()+"\n")
	r.exec.mu.Lock()
	r.exec.units[r.replicaUnit()] = "running"
	r.exec.mu.Unlock()
	r.exec.onStop = func(name string) {
		if name == r.replicaUnit() {
			_ = lock.Release()
			r.writeProc("locks", "")
		}
	}
}

func (r *sqlitePrepRig) writeProc(name, content string) {
	r.t.Helper()
	path := filepath.Join(r.proc, name)
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		r.t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		r.t.Fatal(err)
	}
}

func (r *sqlitePrepRig) host() sqlitePreparationHost {
	session, err := localexec.NewSession(r.exec, uint32(os.Geteuid()), "/", time.Minute) //nolint:gosec // Test runner UID.
	if err != nil {
		r.t.Fatal(err)
	}
	return sqlitePreparationHost{state: r.state, stateRoot: r.root, home: r.home, session: session, executor: r.exec, procRoot: r.proc, cli: restoreTaskCLI{source: r.source}, endpoint: r.remote.server.URL, allowHTTP: true}
}

func (r *sqlitePrepRig) operations() sqlitePreparations {
	service := Service{Store: r.state, Requester: sqlitePrepRequester, Policy: &fakePolicy{}}
	return sqlitePreparations{service: service, jobs: jobs.Service{Store: r.state, Requester: sqlitePrepRequester, Launcher: credentialTestLauncher{}}, host: r.host()}
}

func (r *sqlitePrepRig) runner(o sqlitePreparations) jobs.Runner {
	return jobs.Runner{Store: r.state, TaskHandlers: map[ops.Kind]jobs.TaskHandler{ops.SQLitePrepare: o.task(sqliteprep.PrepareTask), ops.SQLitePrepareAbandon: o.task(sqliteprep.AbandonTask)}}
}

func (r *sqlitePrepRig) key() string {
	r.keys++
	return fmt.Sprintf("%032x", r.keys)
}

// prepare admits and runs one preparation through the real runner.
func (r *sqlitePrepRig) prepare(ctx context.Context, supersedes string) (ops.Operation, *sqliteprep.Receipt) {
	r.t.Helper()
	o := r.operations()
	accepted, err := o.Prepare(context.Background(), dispatch.SQLitePrepareArgs{App: "hello", Database: "main", Supersedes: supersedes, IdempotencyKey: r.key()})
	if err != nil {
		r.t.Fatal("admission", err)
	}
	_ = r.runner(o).Run(ctx, accepted.OperationID)
	return r.outcome(accepted.OperationID)
}

func (r *sqlitePrepRig) abandon(id string) (ops.Operation, *sqliteprep.Receipt) {
	r.t.Helper()
	o := r.operations()
	accepted, err := o.Abandon(context.Background(), dispatch.SQLiteAbandonArgs{PreparationID: id, IdempotencyKey: r.key()})
	if err != nil {
		r.t.Fatal("abandon admission", err)
	}
	_ = r.runner(o).Run(context.Background(), accepted.OperationID)
	return r.outcome(accepted.OperationID)
}

func (r *sqlitePrepRig) outcome(id string) (ops.Operation, *sqliteprep.Receipt) {
	r.t.Helper()
	status, err := (jobs.Service{Store: r.state}).Operation(context.Background(), id, 0)
	if err != nil {
		r.t.Fatal(err)
	}
	if status.Operation.State != ops.Succeeded {
		return status.Operation, nil
	}
	decoded, err := ops.DecodeTaskReceipt(status.Operation, status.Outcome.Receipt)
	if err != nil {
		r.t.Fatal(err)
	}
	receipt := decoded.(sqliteprep.Receipt)
	return status.Operation, &receipt
}

func (r *sqlitePrepRig) preparationID(op ops.Operation) string {
	r.t.Helper()
	_, input, err := r.state.SQLiteTask(context.Background(), op.ID)
	if err != nil {
		r.t.Fatal(err)
	}
	return input.PreparationID
}

func (r *sqlitePrepRig) heldFences() int {
	permit, err := r.state.ReadReplicaPermit(context.Background(), r.database.DatabaseID)
	if err != nil {
		r.t.Fatal(err)
	}
	held := 0
	for _, f := range permit.Fences {
		if f.State == data.FenceHeld {
			held++
		}
	}
	return held
}

// replicaPermit is the real replica ExecStartPre decision, as a boot would make it.
func (r *sqlitePrepRig) replicaPermit() error {
	permits := replicapermits.StorePermits{State: r.state, Configs: replication.DiskConfigs{}}
	return replication.ReplicaPermit(context.Background(), permits, replication.ReplicaPermitRequest{DatabaseID: string(r.database.DatabaseID), BindingID: string(r.replica.BindingID), EpochID: string(r.replica.EpochID), ConfigHash: "sha256:" + r.replica.ConfigSHA256})
}

// writerPermit is the real writer ExecStartPre decision with live unit evidence.
func (r *sqlitePrepRig) writerPermit() error {
	permits := replicapermits.StorePermits{State: r.state, Configs: replication.DiskConfigs{}, Writers: sqlitePrepWriterEvidence{}}
	return replication.WriterPermit(context.Background(), permits, string(r.database.IncarnationID))
}

type sqlitePrepWriterEvidence struct{}

func (sqlitePrepWriterEvidence) OperationActive(context.Context, string) (bool, error) {
	return false, nil
}
func (sqlitePrepWriterEvidence) CommittedUnitsMatch(context.Context, []target.Unit) (bool, error) {
	return true, nil
}

// sqlitePrepExecutor fakes systemctl show/stop, Podman listing and the
// Litestream sync barrier. Every other command, including any start, restart,
// reload or enable, is refused and recorded as forbidden.
type sqlitePrepValidator struct{}

func (sqlitePrepValidator) Validate(ctx context.Context, candidate quadlet.Candidate) error {
	if candidate.UnitName != "hello.container" {
		return errors.New("unexpected fixture writer")
	}
	return ctx.Err()
}

type sqlitePrepExecutor struct {
	home       string
	mu         sync.Mutex
	units      map[string]string
	calls      []string
	forbidden  []string
	dbPath     string
	onStop     func(string)
	containers string
}

const sqlitePrepRunning = "LoadState=loaded\nActiveState=active\nSubState=running\nMainPID=%d\nControlPID=0\nControlGroup=/user.slice/app.slice/%s\nJob=\n"
const sqlitePrepStopped = "LoadState=loaded\nActiveState=inactive\nSubState=dead\nMainPID=0\nControlPID=0\nControlGroup=\nJob=\n"

func (e *sqlitePrepExecutor) Execute(_ context.Context, c localexec.Command) (localexec.Result, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	line := c.Path + " " + strings.Join(c.Args, " ")
	e.calls = append(e.calls, line)
	switch {
	case c.Path == "/usr/bin/systemctl" && len(c.Args) == 5 && c.Args[1] == "show":
		return e.loadedWriter(c.Args[2], c.Args[4]), nil
	case c.Path == "/usr/bin/systemctl" && len(c.Args) == 4 && c.Args[1] == "show":
		if e.units[c.Args[2]] == "running" {
			return localexec.Result{Stdout: fmt.Sprintf(sqlitePrepRunning, e.mainPID(c.Args[2]), c.Args[2])}, nil
		}
		return localexec.Result{Stdout: sqlitePrepStopped}, nil
	case c.Path == "/usr/bin/systemctl" && len(c.Args) == 3 && c.Args[1] == "stop" && c.Mutation:
		e.units[c.Args[2]] = "stopped"
		if e.onStop != nil {
			e.onStop(c.Args[2])
		}
		return localexec.Result{}, nil
	case c.Path == "/usr/bin/podman" && len(c.Args) > 0 && c.Args[0] == "ps":
		return localexec.Result{Stdout: e.containers}, nil
	case c.Path == replication.Executable && len(c.Args) > 0 && c.Args[0] == "sync":
		raw, _ := json.Marshal(map[string]any{"db_path": e.dbPath, "txid": 7, "replica_txid": 7, "duration_ms": 1})
		return localexec.Result{Stdout: string(raw)}, nil
	}
	e.forbidden = append(e.forbidden, line)
	return localexec.Result{ExitCode: 1}, errors.New("sqlite preparation fixture: unexpected command")
}

func (e *sqlitePrepExecutor) mainPID(unit string) int {
	if unit == "hello.service" {
		return sqlitePrepWriterPID
	}
	return sqlitePrepPID
}

func (e *sqlitePrepExecutor) loadedWriter(unit, properties string) localexec.Result {
	if !strings.Contains(properties, "SourcePath") {
		fragment := filepath.Join(e.home, ".config/systemd/user", unit)
		return localexec.Result{Stdout: "FragmentPath=" + fragment + "\nDropInPaths=\nNeedDaemonReload=no\n"}
	}
	source := filepath.Join(e.home, quadlet.ActiveDirectory, "hello.container")
	fragment := filepath.Join("/run/user", fmt.Sprint(os.Geteuid()), "systemd/generator", unit)
	native := filepath.Join(e.home, ".config/systemd/user", unit)
	if _, err := os.Lstat(native); err == nil {
		source, fragment = "", native
	}
	return localexec.Result{Stdout: "SourcePath=" + source + "\nFragmentPath=" + fragment + "\nDropInPaths=\nNeedDaemonReload=no\n"}
}

func (e *sqlitePrepExecutor) count(fragment string) int {
	e.mu.Lock()
	defer e.mu.Unlock()
	n := 0
	for _, call := range e.calls {
		if strings.Contains(call, fragment) {
			n++
		}
	}
	return n
}

func (e *sqlitePrepExecutor) assertNothingStarted(t *testing.T) {
	t.Helper()
	e.mu.Lock()
	defer e.mu.Unlock()
	for _, call := range e.calls {
		if startingCall(call) {
			t.Fatalf("preparation issued %q", call)
		}
	}
	if len(e.forbidden) != 0 {
		t.Fatalf("unexpected commands: %q", e.forbidden)
	}
}

func startingCall(call string) bool {
	for _, verb := range []string{" start ", " restart ", " reload", " enable ", " kill ", " exec "} {
		if strings.Contains(call+" ", verb) {
			return true
		}
	}
	return false
}

// sqlitePrepRemote is an S3-compatible create-only object store. A PUT
// to an existing key answers 412, as the conditional-write capability check expects.
type sqlitePrepRemote struct {
	server  *httptest.Server
	mu      sync.Mutex
	objects map[string][]byte
	puts    int
	gets    int
	onPut   func(*http.Request)
}

func newSQLitePrepRemote(t *testing.T) *sqlitePrepRemote {
	r := &sqlitePrepRemote{objects: map[string][]byte{}}
	r.server = httptest.NewServer(http.HandlerFunc(r.serve))
	t.Cleanup(r.server.Close)
	return r
}

func (r *sqlitePrepRemote) serve(w http.ResponseWriter, req *http.Request) {
	r.mu.Lock()
	hook := r.onPut
	r.mu.Unlock()
	if req.Method == http.MethodPut && hook != nil {
		hook(req)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	switch req.Method {
	case http.MethodPut:
		r.puts++
		if req.Header.Get("If-None-Match") != "*" {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if _, exists := r.objects[req.URL.Path]; exists {
			w.WriteHeader(http.StatusPreconditionFailed)
			return
		}
		body, err := io.ReadAll(req.Body)
		if err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		r.objects[req.URL.Path] = body
		w.WriteHeader(http.StatusCreated)
	case http.MethodGet:
		r.gets++
		body, ok := r.objects[req.URL.Path]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_, _ = w.Write(body)
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

func (r *sqlitePrepRemote) counts() (int, int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.puts, r.gets
}
