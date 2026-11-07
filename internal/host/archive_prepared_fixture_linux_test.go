//go:build linux

package host

import (
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ShaulLavo/brine/internal/archive"
	"github.com/ShaulLavo/brine/internal/caddy"
	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/dispatch"
	"github.com/ShaulLavo/brine/internal/localexec"
	"github.com/ShaulLavo/brine/internal/ops"
	"github.com/ShaulLavo/brine/internal/plan"
	"github.com/ShaulLavo/brine/internal/policy"
	"github.com/ShaulLavo/brine/internal/quadlet"
	"github.com/ShaulLavo/brine/internal/replicapermits"
	"github.com/ShaulLavo/brine/internal/replication"
	"github.com/ShaulLavo/brine/internal/restore"
	"github.com/ShaulLavo/brine/internal/spec"
	"github.com/ShaulLavo/brine/internal/store"
	"github.com/ShaulLavo/brine/internal/systemd"
	"github.com/ShaulLavo/brine/internal/target"
)

type removalHostRig struct {
	prep      *sqlitePrepRig
	host      archiveHost
	service   archive.Service
	plan      archive.Plan
	task      ops.Operation
	inventory *removalInventory
	recorder  *removalRecorder
}

type removalInventory struct {
	snapshot target.Snapshot
	state    *store.Store
	removed  bool
}

func (i *removalInventory) Collect(ctx context.Context) (target.Snapshot, error) {
	snap, err := (&fakeInventory{store: i.state, snapshot: i.snapshot}).Collect(ctx)
	if err != nil {
		return snap, err
	}
	if i.removed {
		snap.CaddyConfig = target.Known(target.CaddyConfigSet{Generation: 1, Files: []target.CaddyFile{}})
	}
	return snap, nil
}

type removalRecorder struct {
	prep               *sqlitePrepExecutor
	inventory          *removalInventory
	retired            bool
	calls              []string
	unitName, unitHash string
	beforeWithdraw     func()
	newWriter          bool
}

func (r *removalRecorder) Execute(ctx context.Context, c localexec.Command) (localexec.Result, error) {
	r.calls = append(r.calls, c.Path+" "+strings.Join(c.Args, " "))
	if c.Path == "/usr/bin/systemctl" && len(c.Args) == 2 && c.Args[1] == "daemon-reload" {
		r.retired = true
		return localexec.Result{}, nil
	}
	if c.Mutation {
		return localexec.Result{}, archive.ErrRefused
	}
	if r.newWriter && c.Path == "/usr/bin/systemctl" && len(c.Args) > 2 && c.Args[2] == "hello.service" {
		return localexec.Result{Stdout: fmt.Sprintf(sqlitePrepRunning, sqlitePrepWriterPID, "hello.service")}, nil
	}
	if r.retired && c.Path == "/usr/bin/systemctl" {
		return localexec.Result{Stdout: strings.Replace(archiveStopped, "LoadState=loaded", "LoadState=not-found", 1)}, nil
	}
	return r.prep.Execute(ctx, c)
}
func (r *removalRecorder) VerifyArchiveRemove(_ context.Context, name, hash string) error {
	if r.unitName != name || r.unitHash != hash || r.retired {
		return archive.ErrRefused
	}
	return nil
}
func (r *removalRecorder) RemoveArchived(ctx context.Context, name, hash string) error {
	if err := r.VerifyArchiveRemove(ctx, name, hash); err != nil {
		return err
	}
	r.calls = append(r.calls, "remove "+name)
	return nil
}
func (r *removalRecorder) InspectArchiveRemove(_ context.Context, name, hash string) (bool, error) {
	if r.unitName != name || r.unitHash != hash {
		return false, archive.ErrRefused
	}
	return !r.retired, nil
}
func (r *removalRecorder) Withdraw(_ context.Context, before caddy.State, app string) (caddy.Result, error) {
	if r.inventory.removed || app != "hello" || len(before.Files) != 1 {
		return caddy.Result{}, archive.ErrRefused
	}
	if r.beforeWithdraw != nil {
		r.beforeWithdraw()
	}
	r.calls = append(r.calls, "withdraw "+app)
	r.inventory.removed = true
	return caddy.Result{Outcome: caddy.Applied, Next: caddy.State{Generation: before.Generation + 1, Files: map[string]string{}}}, nil
}
func (r *removalRecorder) SettleWithdrawal(context.Context, caddy.State, caddy.State, string) error {
	return nil
}

func removalPolicy(t *testing.T, root data.PersistentRoot, e data.RetentionEvidence) policy.Policy {
	t.Helper()
	raw := fmt.Sprintf(`schema_version=1
version="removal-fixture"
persistent_roots=[%q]
[resources]
memory_mb=512
pids_limit=128
[sqlite_archive_removal]
max_backup_age_seconds=900
max_restore_test_age_seconds=300
recovery_window_seconds=86400
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
verification_id=%q
verified_at=%q
freshness_seconds=86400
no_object_expiration=true
`, string(root), e.VerificationID, e.VerifiedAt)
	p, err := policy.Parse([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func newRemovalHostRig(t *testing.T) *removalHostRig { return newRemovalHostRigMode(t, false) }

func newRemovalHostRigMode(t *testing.T, two bool) *removalHostRig {
	return newRemovalHostRigSource(t, two, false)
}

func newRemovalHostRigSource(t *testing.T, two, running bool) *removalHostRig {
	t.Helper()
	ctx := t.Context()
	sources, pol := newRemovalSources(t, two)
	prep := sources[0]
	if running {
		prep.startSupervisor()
	}
	prepareRemovalSources(t, sources)
	raw, err := os.ReadFile("../target/testdata/ready-arm64.json")
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := target.Decode(raw)
	if err != nil {
		t.Fatal(err)
	}
	inv := &removalInventory{state: prep.state, snapshot: snapshot}
	current, err := prep.state.CurrentRelease(ctx, "hello")
	if err != nil {
		t.Fatal(err)
	}
	recorder := &removalRecorder{prep: prep.exec, inventory: inv, unitName: current.Units[0].Name, unitHash: current.Units[0].Hash}
	session, err := localexec.NewSession(recorder, 1000, "/", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	h := archiveHost{removal: true, service: Service{Store: prep.state, Requester: sqlitePrepRequester, Policy: &fakePolicy{p: pol}, Inventory: inv}, stateRoot: prep.root, home: prep.home, session: session, executor: recorder, routes: recorder, units: recorder}
	s := newArchiveService(h.service, h.stateRoot, h.home, session, recorder, recorder, recorder)
	h.restoreTesting = &archiveRestoreTesting{cli: restoreTaskCLI{source: prep.source}, endpoint: prep.remote.server.URL}
	h.removalVerified = map[string]restore.SQLiteReceipt{}
	s.Host = h
	p, err := s.Plan(ctx, "hello")
	if err != nil {
		t.Fatal("removal planning", err)
	}
	r := &removalHostRig{prep: prep, host: h, service: s, plan: p, inventory: inv, recorder: recorder}
	r.task = r.newTask(t, ops.SQLiteRetire, "", "")
	r.service.Operation = r.task.ID
	return r
}

func (r *removalHostRig) newTask(t *testing.T, kind ops.Kind, original, decision string) ops.Operation {
	t.Helper()
	ctx := t.Context()
	id, err := r.prep.state.SaveSQLiteRetireInput(ctx, store.SQLiteRetireInput{Kind: kind, App: "hello", PlanID: r.plan.ID, OriginalOperationID: original, Disposition: decision})
	if err != nil {
		t.Fatal(err)
	}
	op, _, err := r.prep.state.CreateOperation(ctx, ops.Intent{Kind: kind, App: "hello", SecretRef: id, RecoveryOf: original}, sqlitePrepRequester, r.prep.key())
	if err != nil {
		t.Fatal(err)
	}
	if err = r.prep.state.TransitionOperation(ctx, op.ID, ops.Queued, ops.Preflight); err != nil {
		t.Fatal(err)
	}
	op.State = ops.Preflight
	return op
}

func (r *removalHostRig) resolve(t *testing.T, decision string) {
	t.Helper()
	if err := r.prep.state.MarkSQLiteRetireRecovery(t.Context(), r.task, "interrupted"); err != nil {
		t.Fatal(err)
	}
	r.task = r.newTask(t, ops.SQLiteRetireResolve, r.task.ID, decision)
	r.service.Operation = r.task.ID
}

func (r *removalHostRig) noWriterEffects(t *testing.T) {
	t.Helper()
	for _, call := range r.recorder.calls {
		if strings.Contains(call, " stop ") || strings.Contains(call, " start ") || strings.Contains(call, " restart ") || strings.Contains(call, " sync ") {
			t.Fatal("removal changed a writer or uploaded", call)
		}
	}
}

func (r *removalHostRig) receipt(t *testing.T) archive.RemovalReceipt {
	t.Helper()
	op, err := r.prep.state.GetOperation(t.Context(), r.task.ID)
	if err != nil {
		t.Fatal(err)
	}
	receipt, ok, err := r.prep.state.ReadSQLiteRetireSuccess(t.Context(), op)
	if err != nil || !ok {
		t.Fatal("committed removal receipt", err)
	}
	return receipt
}

func replaceRemovalWAL(t *testing.T, r *removalHostRig) {
	t.Helper()
	path := filepath.Join(string(r.prep.database.Root), r.prep.database.RelativeDirectory, string(r.prep.database.Filename)) + "-wal"
	if err := os.WriteFile(path, []byte("replacement WAL"), 0600); err != nil {
		t.Fatal(err)
	}
}

func newRemovalSources(t *testing.T, two bool) ([]*sqlitePrepRig, policy.Policy) {
	t.Helper()
	_, desired, facts := persistentHostFixture(t)
	desired.ContainerPort, desired.Domains = 3000, []spec.Domain{"hello.example.com"}
	desired.Health = policy.Health{Path: "/", ExpectedStatus: 200, StartupDeadlineSeconds: 30, TimeoutSeconds: 3}
	desired.Resources = policy.Resources{MemoryMB: 512, PIDsLimit: 128}
	desired.SchemaCompatibility = []data.SQLiteSchemaCompatibility{{Database: "main", Startup: "preserve", Accepts: []string{"v1"}}}
	desired.SchemaDefinitions = []data.SQLiteSchemaDefinition{{Database: "main", Marker: "v1", CatalogSHA256: sqlitePrepCatalog(t, sqlitePrepSchemas["v1"])}}
	pol := removalPolicy(t, desired.Databases[0].PersistentRoot, desired.BackupRetention[0])
	desired.PolicyHash = pol.Hash()
	r := &sqlitePrepRig{t: t, root: shortReplicaStateRoot(t), proc: t.TempDir(), home: privateRemovalDirectory(t)}
	state, err := store.OpenContext(t.Context(), r.root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = state.Close() })
	r.state = state
	facts.Store, facts.StateRoot = state, r.root
	r.database = collectPersistent(t, facts, desired).Database
	sources := []*sqlitePrepRig{r}
	if two {
		sources = append(sources, secondRemovalSource(t, r, facts, &desired))
	}
	if err = state.RecordWriterAttempt(t.Context(), r.database.IncarnationID, "removal-fixture-writer"); err != nil {
		t.Fatal(err)
	}
	records := make([]store.CredentialRecord, 0, len(sources))
	for _, source := range sources {
		records = append(records, provisionRemovalSource(t, source, desired))
	}
	prepareRemovalReplicas(t, sources, facts, desired, records)
	commitRemovalRelease(t, r, desired, facts)
	executor := &sqlitePrepExecutor{home: r.home, units: map[string]string{"hello.service": "running"}, dbPath: r.path("")}
	for _, source := range sources {
		finishRemovalSource(t, source, executor)
	}
	return sources, pol
}

func provisionRemovalSource(t *testing.T, r *sqlitePrepRig, desired policy.Desired) store.CredentialRecord {
	t.Helper()
	writeSQLitePrepDatabase(t, r.path(""), "v1")
	permit, err := r.state.ReadReplicaPermit(t.Context(), r.database.DatabaseID)
	if err != nil {
		t.Fatal(err)
	}
	received := time.Now().UTC().Add(-time.Minute)
	expires := received.Add(2 * time.Hour)
	id, err := data.NewID()
	if err != nil {
		t.Fatal(err)
	}
	record := store.CredentialRecord{ID: id, Kind: "receipt", App: "hello", CredentialRef: "primary", Requester: "fixture", PlanID: "sha256:" + strings.Repeat("f", 64), PlanHash: "sha256:" + strings.Repeat("f", 64), TargetHash: "sha256:" + strings.Repeat("b", 64), PolicyHash: desired.PolicyHash, BindingID: permit.Replica.BindingID, Destination: "primary", EpochID: permit.Replica.EpochID, Version: 1, ReceivedAt: received, ExpiresAt: &expires}
	if err = ensurePrivateChild(r.root, "credentials/s3/primary"); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(r.root, "credentials/s3/primary/v1.env"), []byte("AWS_ACCESS_KEY_ID=fixture-access\nAWS_SECRET_ACCESS_KEY=fixture-secret\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err = r.state.SaveCredentialRecord(t.Context(), record); err != nil {
		t.Fatal(err)
	}
	return record
}

func finishRemovalSource(t *testing.T, r *sqlitePrepRig, executor *sqlitePrepExecutor) {
	t.Helper()
	raw, err := os.ReadFile(r.path(""))
	if err != nil {
		t.Fatal(err)
	}
	r.source, r.exec = raw, executor
	executor.units[r.replicaUnit()] = "stopped"
	r.writeProc(fmt.Sprintf("%d/cgroup", sqlitePrepWriterPID), "0::/user.slice/app.slice/hello.service\n")
	r.remote = newSQLitePrepRemote(t)
}

func secondRemovalSource(t *testing.T, r *sqlitePrepRig, facts DataFacts, desired *policy.Desired) *sqlitePrepRig {
	t.Helper()
	b := addEmptyRemovalDatabase(t, r.state, facts, *desired)
	d := desired.Databases[0]
	d.Name, d.MountPath, d.Filename = b.Name, b.MountPath, b.Filename
	desired.Databases = append(desired.Databases, d)
	desired.SchemaCompatibility = append(desired.SchemaCompatibility, data.SQLiteSchemaCompatibility{Database: b.Name, Startup: "preserve", Accepts: []string{"v1"}})
	desired.SchemaDefinitions = append(desired.SchemaDefinitions, data.SQLiteSchemaDefinition{Database: b.Name, Marker: "v1", CatalogSHA256: sqlitePrepCatalog(t, sqlitePrepSchemas["v1"])})
	if err := r.state.RegisterSchemaDefinitions(t.Context(), b.IncarnationID, desired.SchemaDefinitions); err != nil {
		t.Fatal(err)
	}
	return &sqlitePrepRig{t: t, state: r.state, root: r.root, home: r.home, proc: r.proc, database: b, keys: 1000}
}

func prepareRemovalSources(t *testing.T, sources []*sqlitePrepRig) {
	t.Helper()
	for _, r := range sources {
		prepareRemovalSource(t, r)
	}
}
func prepareRemovalSource(t *testing.T, r *sqlitePrepRig) {
	t.Helper()
	o := r.operations()
	args := dispatch.SQLitePrepareArgs{App: "hello", Database: string(r.database.Name), IdempotencyKey: r.key()}
	accepted, err := o.Prepare(t.Context(), args)
	if err != nil {
		t.Fatal(err)
	}
	if err = r.runner(o).Run(t.Context(), accepted.OperationID); err != nil {
		t.Fatal("genuine preparation runner", err)
	}
	operation, receipt := r.outcome(accepted.OperationID)
	if operation.State != ops.Succeeded || receipt == nil {
		t.Fatal("genuine preparation", operation.State)
	}
}

func prepareRemovalReplicas(t *testing.T, sources []*sqlitePrepRig, facts DataFacts, d policy.Desired, records []store.CredentialRecord) {
	t.Helper()
	r := sources[0]
	manager := &systemd.Fake{DaemonReloadFunc: func(context.Context) error { return nil }, JobPendingFunc: func(context.Context, systemd.Unit) (bool, error) { return false, nil }, ShowFunc: func(context.Context, systemd.Unit) (systemd.Properties, error) {
		return systemd.Properties{ActiveState: "active", SubState: "running"}, nil
	}}
	permits := replicapermits.StorePermits{State: r.state, Configs: replication.DiskConfigs{}}
	preparation := DataPreparation{Runner: &preparationPullRunner{}, ProbeRoot: facts.ProbeRoot, ProbeMapping: facts.ProbeMapping, State: r.state, StateRoot: r.root, Home: r.home, Permits: permits, Publisher: replication.ArtifactPublisher{StateRoot: r.root, UnitRoot: filepath.Join(r.home, ".config/systemd/user")}, Services: &preparedServices{}, Units: manager}
	planned := plan.Plan{DataCredentials: []data.CredentialEvidence{}, DataMounts: []data.Mount{}}
	for i, source := range sources {
		record := records[i]
		planned.DataCredentials = append(planned.DataCredentials, data.CredentialEvidence{BindingID: record.BindingID, EpochID: record.EpochID, Destination: record.Destination, Reference: record.CredentialRef, Version: record.Version, PolicyHash: record.PolicyHash, ReceivedAt: record.ReceivedAt, ExpiresAt: record.ExpiresAt})
		planned.DataMounts = append(planned.DataMounts, data.Mount{Database: source.database, HostPath: source.path("-dir"), ContainerPath: source.database.MountPath, BindingID: source.database.ReplicaBindingID})
	}
	if err := preparation.PreparePersistent(t.Context(), "fixture", planned, d); err != nil {
		t.Fatal("prepare genuine replicas", err)
	}
	for _, source := range sources {
		permit, err := r.state.ReadReplicaPermit(t.Context(), source.database.DatabaseID)
		if err != nil || !permit.Replica.Committed {
			t.Fatal("genuine replica", err)
		}
		source.replica = permit.Replica
	}
}

func commitRemovalRelease(t *testing.T, r *sqlitePrepRig, desired policy.Desired, facts DataFacts) {
	t.Helper()
	p := removalReleasePlan(t, r, desired, facts)
	unit, err := quadlet.Render(desired, p, *p.Image.ManifestDigest.Value)
	if err != nil {
		t.Fatal(err)
	}
	manager, err := quadlet.NewManager(r.home, sqlitePrepValidator{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = manager.Close() }()
	if err = manager.Install(t.Context(), unit, ""); err != nil {
		t.Fatal(err)
	}
	id, err := r.state.SavePlan(t.Context(), p, desired)
	if err != nil {
		t.Fatal(err)
	}
	if p.HostPort != 20000 {
		t.Fatal("unexpected allocated fixture port", p.HostPort)
	}
	site, err := caddy.CommittedSite(desired, 20000)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := caddy.Render(site)
	if err != nil {
		t.Fatal(err)
	}
	release := store.Release{ID: "removal-source-release", PlanID: id, Image: p.Image, HostPort: p.HostPort, Secrets: p.Secrets, Units: []target.Unit{{Name: unit.Name(), Hash: unit.Hash()}}, CaddyFile: target.CaddyFile{Name: "hello.caddy", Hash: fmt.Sprintf("sha256:%x", sha256.Sum256(raw))}}
	if err = r.state.CommitRelease(t.Context(), "hello", release); err != nil {
		t.Fatal(err)
	}
}
func removalReleasePlan(t *testing.T, r *sqlitePrepRig, desired policy.Desired, facts DataFacts) plan.Plan {
	t.Helper()
	raw, err := os.ReadFile("../target/testdata/ready-arm64.json")
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := target.Decode(raw)
	if err != nil {
		t.Fatal(err)
	}
	persistent, err := facts.Collect(t.Context(), desired)
	if err != nil {
		t.Fatal(err)
	}
	snapshot.PersistentData = &persistent
	image := plan.Image{Digest: strings.Split(string(desired.Image), "@")[1], Platform: target.Platform{OS: "linux", Arch: snapshot.Arch}, ManifestDigest: target.Known("sha256:" + strings.Repeat("b", 64))}
	in := plan.Input{Desired: desired, Snapshot: snapshot, Image: image, State: plan.BrineState{Target: snapshot.Identity, Generation: *snapshot.Generation.Value, Releases: []plan.CurrentRelease{}}}
	p, err := plan.Build(in)
	if err != nil || p.Kind == plan.Conflict {
		t.Fatal("real source release", p.Conflicts, err)
	}
	return p
}

func newEmptyRemovalHostRig(t *testing.T, two bool) *removalHostRig {
	t.Helper()
	_, desired, facts := persistentHostFixture(t)
	root := shortReplicaStateRoot(t)
	state, err := store.OpenContext(t.Context(), root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = state.Close() })
	pol := removalPolicy(t, desired.Databases[0].PersistentRoot, desired.BackupRetention[0])
	desired.PolicyHash = pol.Hash()
	facts.Store, facts.StateRoot = state, root
	first := collectPersistent(t, facts, desired)
	if two {
		addEmptyRemovalDatabase(t, state, facts, desired)
	}
	prep := &sqlitePrepRig{t: t, state: state, root: root, home: privateRemovalDirectory(t), database: first.Database, exec: &sqlitePrepExecutor{units: map[string]string{}}}
	inv := &removalInventory{state: state, snapshot: target.Snapshot{Apps: target.Known([]target.App{}), CaddyConfig: target.Known(target.CaddyConfigSet{Files: []target.CaddyFile{}})}}
	recorder := &removalRecorder{prep: prep.exec, inventory: inv, retired: true}
	session, err := localexec.NewSession(recorder, 1000, "/", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	h := archiveHost{removal: true, service: Service{Store: state, Requester: sqlitePrepRequester, Policy: &fakePolicy{p: pol}, Inventory: inv}, stateRoot: root, home: prep.home, session: session, executor: recorder}
	s := newArchiveService(h.service, root, prep.home, session, recorder, nil, nil)
	s.Host = h
	p, err := s.Plan(t.Context(), "hello")
	if err != nil {
		t.Fatal("empty removal plan", err)
	}
	r := &removalHostRig{prep: prep, host: h, service: s, plan: p, inventory: inv, recorder: recorder}
	r.task = r.newTask(t, ops.SQLiteRetire, "", "")
	r.service.Operation = r.task.ID
	return r
}
func addEmptyRemovalDatabase(t *testing.T, state *store.Store, facts DataFacts, desired policy.Desired) data.SQLiteDatabaseBinding {
	t.Helper()
	declaration := desired.Databases[0]
	declaration.Name, declaration.MountPath, declaration.Filename = "second", "/second", "second.db"
	reserved, err := state.ReserveDatabase(t.Context(), store.DataReservation{App: "hello", PolicyHash: desired.PolicyHash, Database: declaration, Destination: desired.BackupDestinations[0]})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = data.PrepareDirectory(reserved.Database); err != nil {
		t.Fatal(err)
	}
	allocation, err := data.CaptureAllocation(reserved.Database)
	if err != nil {
		t.Fatal(err)
	}
	proof, err := facts.ProbeRoot(t.Context(), string(declaration.PersistentRoot))
	if err != nil {
		t.Fatal(err)
	}
	mapping, err := facts.ProbeMapping(t.Context(), nil, proof, *desired.Runtime)
	if err != nil {
		t.Fatal(err)
	}
	allocation.RootProof, allocation.MappingProof = &proof, &mapping
	if err = state.RecordAllocation(t.Context(), allocation); err != nil {
		t.Fatal(err)
	}
	return reserved.Database
}

func privateRemovalDirectory(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "private")
	if err := os.Mkdir(path, 0700); err != nil {
		t.Fatal(err)
	}
	return path
}
