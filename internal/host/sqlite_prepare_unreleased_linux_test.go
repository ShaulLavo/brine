//go:build linux

package host

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ShaulLavo/brine/internal/archive"
	"github.com/ShaulLavo/brine/internal/backupcredentials"
	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/datainit"
	"github.com/ShaulLavo/brine/internal/dispatch"
	"github.com/ShaulLavo/brine/internal/inventory"
	"github.com/ShaulLavo/brine/internal/jobs"
	"github.com/ShaulLavo/brine/internal/ops"
	"github.com/ShaulLavo/brine/internal/policy"
	"github.com/ShaulLavo/brine/internal/replication"
	"github.com/ShaulLavo/brine/internal/restore"
	"github.com/ShaulLavo/brine/internal/spec"
	"github.com/ShaulLavo/brine/internal/sqliteprep"
	"github.com/ShaulLavo/brine/internal/store"
	"github.com/ShaulLavo/brine/internal/target"
)

type sqliteIdentityFS struct{ root string }

func (f sqliteIdentityFS) ReadFile(ctx context.Context, name string) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return os.ReadFile(filepath.Join(f.root, filepath.Base(name)))
}
func (sqliteIdentityFS) ReadDir(context.Context, string) ([]fs.DirEntry, error) {
	return nil, fs.ErrNotExist
}
func (sqliteIdentityFS) Readlink(context.Context, string) (string, error) { return "", fs.ErrNotExist }

type unreleasedSQLiteRig struct {
	physical   *sqlitePrepRig
	deploy     *deployRig
	operations sqlitePreparations
	plan       string
	request    datainit.Request
	credential backupcredentials.Receipt
	collector  inventory.Collector
}

func newUnreleasedSQLiteRig(t *testing.T) *unreleasedSQLiteRig {
	t.Helper()
	r, _, _, _ := connectedPreparationRig(t)
	app, err := spec.Parse([]byte(r.spec))
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile("../policy/testdata/operator.toml")
	if err != nil {
		t.Fatal(err)
	}
	text := strings.ReplaceAll(string(raw), "Registry.Example.com:5000", "ghcr.io")
	text = strings.ReplaceAll(text, `persistent_roots = ["/srv/brine/data"]`, fmt.Sprintf("persistent_roots = [%q]", app.Databases[0].PersistentRoot))
	text += fmt.Sprintf(`
[data_initialization]
max_backup_age_seconds=300
max_restore_test_age_seconds=300
recovery_window_seconds=3600
[sqlite_archive_removal]
max_backup_age_seconds=300
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
verification_id="11111111111111111111111111111111"
verified_at=%q
freshness_seconds=3600
no_object_expiration=true
`, time.Now().UTC().Format(time.RFC3339Nano))
	r.policy.p, err = policy.Parse([]byte(text))
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	blob := append([]byte("\x00\x00\x00\x0bssh-ed25519\x00\x00\x00\x20"), []byte(strings.Repeat("x", 32))...)
	for name, value := range map[string]string{"machine-id": strings.Repeat("1", 32), "ssh_host_ed25519_key.pub": "ssh-ed25519 " + base64.StdEncoding.EncodeToString(blob)} {
		if err = os.WriteFile(filepath.Join(root, name), []byte(value), 0600); err != nil {
			t.Fatal(err)
		}
	}
	collector := inventory.Collector{FS: sqliteIdentityFS{root}, IdentityKey: []byte("fixture-identity-key")}
	r.inventory.snapshot.Identity, err = collector.Identity(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	r.service.Requester = sqlitePrepRequester
	initializer := data.SchemaInitializer{Definition: data.SQLiteSchemaDefinition{Database: "main", Marker: "v1", CatalogSHA256: "688d95e9133c228079e32bcbdad7325064146b7b1be403a7bbe4a8b83a9c4134"}, Statements: []string{"CREATE TABLE t(x TEXT)"}}
	artifact, body, err := initializer.Artifact()
	if err != nil {
		t.Fatal(err)
	}
	app.SchemaDefinitions = []data.SQLiteSchemaDefinition{initializer.Definition}
	app.SchemaArtifacts = []data.SchemaArtifact{artifact}
	app.Initializers = []data.SchemaInitializer{initializer}
	app.SchemaCompatibility[0].Accepts = []string{"v1"}
	app.RestoreInvariants = []data.RestoreInvariant{{Database: "main", Kind: "row_count", Table: "t", Count: 0}}
	planned, err := r.service.PlanDataPreparation(t.Context(), app)
	if err != nil {
		t.Fatal(err)
	}
	accepted := r.call(t, "apply", dispatch.ApplyArgs{PlanID: planned.PlanID, IdempotencyKey: "unreleased-allocation"}).Data.(jobs.Accepted)
	if err = r.runner.Run(t.Context(), accepted.OperationID); err != nil {
		t.Fatal(err)
	}
	p, _, err := r.store.LoadPlan(t.Context(), planned.PlanID)
	if err != nil {
		t.Fatal(err)
	}
	physical := &sqlitePrepRig{t: t, state: r.store, root: r.stateRoot, home: t.TempDir(), database: p.DataAllocations[0].Database, replica: p.DataAllocations[0].Replica, proc: t.TempDir(), remote: newSQLitePrepRemote(t)}
	physical.exec = &sqlitePrepExecutor{unpublished: true, units: map[string]string{}, home: physical.home, dbPath: physical.path("")}
	h := physical.host()
	h.policy, h.identity = r.policy, collector.Identity
	operations := sqlitePreparations{service: r.service, jobs: jobs.Service{Store: r.store, Requester: sqlitePrepRequester, Launcher: credentialTestLauncher{}}, host: h}
	rig := &unreleasedSQLiteRig{physical: physical, deploy: r, operations: operations, plan: planned.PlanID, request: datainit.Request{App: "hello", FirstReleasePlan: planned.PlanID, Artifact: artifact.Digest, ArtifactBytes: body}, collector: collector}
	rig.deliver(t, nil)
	return rig
}

func (r *unreleasedSQLiteRig) deliver(t *testing.T, expires *time.Time) {
	t.Helper()
	service := backupCredentialService(r.deploy.service, r.physical.root, "main")
	p, err := service.Plan(t.Context(), "hello", expires)
	if err != nil {
		t.Fatal(err)
	}
	raw := `{"access_key_id":"fixture-access","secret_access_key":"fixture-secret"}`
	if expires != nil {
		raw = strings.TrimSuffix(raw, "}") + `,"expires_at":"` + expires.Format(time.RFC3339Nano) + `"}`
	}
	packet, err := backupcredentials.DecodePacket([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	defer packet.Clear()
	r.credential, err = service.Deliver(t.Context(), p, packet)
	if err != nil {
		t.Fatal(err)
	}
}

func (r *unreleasedSQLiteRig) prepare(t *testing.T, supersedes string) (ops.Operation, *sqliteprep.Receipt) {
	t.Helper()
	args := dispatch.SQLitePrepareArgs{App: "hello", Database: "main", Supersedes: supersedes, IdempotencyKey: r.physical.key()}
	if supersedes == "" {
		args.DeclarationPlanID, args.DeliveredCredentialVersion = r.plan, r.credential.Version
	}
	accepted, err := r.operations.Prepare(t.Context(), args)
	if err != nil {
		t.Fatal(err)
	}
	runErr := r.physical.runner(r.operations).Run(t.Context(), accepted.OperationID)
	op, receipt := r.physical.outcome(accepted.OperationID)
	if receipt == nil {
		t.Logf("runner refusal: %v", runErr)
	}
	return op, receipt
}

func TestSQLiteAllocatedUnreleasedThroughRealRunner(t *testing.T) {
	r := newUnreleasedSQLiteRig(t)
	schema := unreleasedSchemaFingerprint(t, r.physical.root)
	op, receipt := r.prepare(t, "")
	if receipt == nil {
		t.Fatalf("allocated preparation failed: %s", op.State)
	}
	source, err := r.deploy.store.ReadSQLitePreparedSource(t.Context(), "hello", "main", receipt.PreparationID)
	if err != nil || source.Scope.Schema.State != data.AllocatedEmpty || source.Point.Schema.State != data.VerifiedEmpty || source.Scope.ReplicaCommitted || source.Scope.CredentialVersion == 0 || source.Scope.ActivatedCredentialVersion != 0 || len(source.Scope.Checks) != 0 {
		t.Fatalf("allocated source: %+v %v", source, err)
	}
	raw, _ := json.Marshal(r.deploy.inventory.snapshot.Identity)
	if "sha256:"+sqliteHashForTest(raw) == "sha256:"+source.Scope.TargetSHA256 {
		t.Fatal("host and destination hashes conflated")
	}
	entries, err := os.ReadDir(r.physical.path("-dir"))
	if err != nil || len(entries) != 0 {
		t.Fatal("live source changed", entries, err)
	}
	puts, gets := r.physical.remote.counts()
	if puts != 2 || gets != 1 {
		t.Fatalf("remote effects %d %d", puts, gets)
	}
	_, successor := r.prepare(t, receipt.PreparationID)
	if successor == nil || successor.SourcePointID != receipt.SourcePointID || successor.Fence != receipt.Fence {
		t.Fatal("reverify changed origin", successor)
	}
	puts2, _ := r.physical.remote.counts()
	if puts2 != puts {
		t.Fatal("reverify uploaded")
	}
	if err = os.Remove(filepath.Join(r.physical.root, "credentials/s3/primary/v1.env")); err != nil {
		t.Fatal(err)
	}
	accepted, err := r.operations.Abandon(t.Context(), dispatch.SQLiteAbandonArgs{PreparationID: successor.PreparationID, IdempotencyKey: r.physical.key()})
	if err != nil {
		t.Fatal(err)
	}
	if err = r.physical.runner(r.operations).Run(t.Context(), accepted.OperationID); err != nil {
		t.Fatal("packet-free abandon", err)
	}
	if r.physical.heldFences() != 0 {
		t.Fatal("fence retained")
	}
	r.physical.exec.assertNothingStarted(t)
	if r.physical.exec.count(" stop ") != 0 {
		t.Fatal("reserved branch stopped a service")
	}
	if after := unreleasedSchemaFingerprint(t, r.physical.root); after != schema {
		t.Fatal("preparation changed schema objects")
	}
}

func sqliteHashForTest(raw []byte) string {
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func (r *unreleasedSQLiteRig) initializer(t *testing.T) datainit.Service {
	t.Helper()
	engine := dataInitializationService(r.deploy.service, r.physical.root)
	engine.PrepareRestorePoint = func(ctx context.Context, p datainit.Plan, op datainit.Operation) (datainit.VerifiedRestorePoint, error) {
		root := filepath.Join(r.physical.root, "unreleased-init-staging")
		if err := ensurePrivateChild(r.physical.root, "unreleased-init-staging"); err != nil {
			return datainit.VerifiedRestorePoint{}, err
		}
		record, err := r.deploy.store.CredentialReceipt(ctx, r.physical.replica.BindingID, r.credential.Version)
		if err != nil {
			return datainit.VerifiedRestorePoint{}, err
		}
		credentials, err := readDeliveredCredentials(r.physical.root, record)
		if err != nil {
			return datainit.VerifiedRestorePoint{}, err
		}
		defer func() { credentials = restore.Credentials{} }()
		binding := withTestEndpoint(restoreBinding(r.physical.replica), r.physical.remote.server.URL)
		source, uploaded, err := restore.CreateEmptySnapshot(ctx, root, binding, credentials, p.RestorePointID, time.Minute, true)
		if err != nil {
			return datainit.VerifiedRestorePoint{}, err
		}
		verifier := restore.SQLiteEngine{Root: root, MaxSnapshotBytes: 1 << 20, AllowHTTPForTests: true, Observer: restore.EmptySchemaObserver{}, Bindings: restore.BindingReaderFunc(func(context.Context, string, string) (restore.Binding, error) { return binding, nil }), Credentials: restore.CredentialReaderFunc(func(context.Context, string) (restore.Credentials, error) { return credentials, nil })}
		receipt, err := checkSQLiteRestore(ctx, &verifier, restore.SQLiteRequest{OperationID: op.ID + "-empty-verify", Source: restore.SQLiteSource{Kind: restore.SQLiteSnapshot, Snapshot: &source}, Budget: time.Minute, ExpectedSchema: restore.SQLiteSchemaObservation{State: restore.VerifiedEmpty, Marker: data.EmptyMarker, CatalogSHA256: data.EmptyCatalogSHA256}})
		if err != nil {
			return datainit.VerifiedRestorePoint{}, err
		}
		return datainit.VerifiedRestorePoint{PointID: p.RestorePointID, DatabaseID: p.Database.DatabaseID, UploadedAt: uploaded, RetainUntil: uploaded.Add(2 * time.Hour), Receipt: receipt}, nil
	}
	return engine
}

func (r *unreleasedSQLiteRig) initialize(t *testing.T) {
	t.Helper()
	engine := r.initializer(t)
	p, err := engine.Plan(t.Context(), r.request)
	if err != nil {
		t.Fatal("initialization plan", err)
	}
	operations := initializationOperations{engine: engine, tasks: r.operations.jobs}
	accepted, err := operations.Apply(t.Context(), "hello", p.ID)
	if err != nil {
		t.Fatal("initialization admission", err)
	}
	runner := jobs.Runner{Store: r.deploy.store, TaskHandlers: map[ops.Kind]jobs.TaskHandler{ops.DataInitApply: func(ctx context.Context, op ops.Operation) (json.RawMessage, error) {
		initialized, err := engine.Apply(ctx, op.App, op.SecretRef)
		if err != nil {
			return nil, err
		}
		return json.Marshal(initialized)
	}}}
	if err = runner.Run(t.Context(), accepted.OperationID); err != nil {
		t.Fatal("initialization runner", err)
	}
}

func TestSQLiteInitializedUnreleasedThroughRealRunner(t *testing.T) {
	r := newUnreleasedSQLiteRig(t)
	r.initialize(t)
	puts, gets := r.physical.remote.counts()
	if puts != 2 || gets != 1 {
		t.Fatal("initializer did not independently verify real empty bytes", puts, gets)
	}
	op, receipt := r.prepare(t, "")
	if receipt == nil {
		t.Fatal("initialized prepare", op.State)
	}
	source, err := r.deploy.store.ReadSQLitePreparedSource(t.Context(), "hello", "main", receipt.PreparationID)
	if err != nil || source.Scope.Schema.Marker != "v1" || source.Scope.EmptyAllocation != nil || source.Point.Schema.State != data.VerifiedSchema || len(source.Scope.Checks) != 1 {
		t.Fatal("initialized source", source, err)
	}
	tests := restoreTests{stateDir: r.physical.root, endpoint: r.physical.remote.server.URL, allowHTTP: true}
	checked, err := tests.Check(t.Context(), dispatch.RestoreTestArgs{App: "hello", Database: "main", Point: receipt.PreparationID}, strings.Repeat("9", 32))
	if err != nil || checked.Schema.Marker != "v1" {
		t.Fatal("selected initialized restore", checked, err)
	}
	r.physical.exec.assertNothingStarted(t)
}

func TestSQLiteUnreleasedPreclaimRefusals(t *testing.T) {
	cases := []string{"missing-selected-with-newer", "short-original-budget", "foreign-host", "extra-entry", "orphan-wal", "loaded-stopped-unit", "foreign-lifetime", "other-pending-task", "initialized-missing", "writer-history", "pending-init", "host-policy-drift"}
	for _, name := range cases {
		t.Run(name, func(t *testing.T) { checkUnreleasedPreclaimRefusal(t, name) })
	}
}

func checkUnreleasedPreclaimRefusal(t *testing.T, name string) {
	t.Helper()
	r := newUnreleasedSQLiteRig(t)
	switch name {
	case "missing-selected-with-newer":
		original := r.credential
		r.deliver(t, nil)
		r.credential = original
		if err := os.Remove(filepath.Join(r.physical.root, "credentials/s3/primary/v1.env")); err != nil {
			t.Fatal(err)
		}
	case "short-original-budget":
		expires := time.Now().UTC().Add(10 * time.Minute)
		r.deliver(t, &expires)
	case "foreign-host":
		r.operations.host.identity = func(ctx context.Context) (target.Identity, error) {
			c := r.collector
			c.IdentityKey = []byte("another-fixture-key")
			return c.Identity(ctx)
		}
	case "extra-entry", "orphan-wal":
		path := filepath.Join(r.physical.path("-dir"), "unexpected")
		if name == "orphan-wal" {
			path = r.physical.path("-wal")
		}
		if err := os.WriteFile(path, []byte("not data"), 0600); err != nil {
			t.Fatal(err)
		}
	case "loaded-stopped-unit":
		r.physical.exec.units["hello.service"] = "stopped"
	case "foreign-lifetime":
		retainUnreleasedForeignLifetime(t, r)
	case "other-pending-task":
		_, err := r.operations.Prepare(t.Context(), dispatch.SQLitePrepareArgs{App: "hello", Database: "main", DeclarationPlanID: r.plan, DeliveredCredentialVersion: r.credential.Version, IdempotencyKey: r.physical.key()})
		if err != nil {
			t.Fatal(err)
		}
	case "writer-history":
		if err := r.deploy.store.RecordWriterAttempt(t.Context(), r.physical.database.IncarnationID, "fixture-writer-attempt"); err != nil {
			t.Fatal(err)
		}
	case "pending-init":
		r.interruptedInitialization(t, "mutation_intent")
	case "host-policy-drift":
		r.operations.host.policy = changedUnreleasedPolicy{}
	case "initialized-missing":
		r.initialize(t)
		if err := os.Remove(r.physical.path("")); err != nil {
			t.Fatal(err)
		}
	}
	before, _ := r.physical.remote.counts()
	fencesBefore := r.physical.heldFences()
	op, receipt := r.prepare(t, "")
	after, _ := r.physical.remote.counts()
	if receipt != nil || op.State != ops.Failed || r.physical.heldFences() != fencesBefore || before != after || r.physical.exec.count(" stop ") != 0 {
		t.Fatalf("unsafe admission %s receipt=%v before=%d after=%d fences=%d", op.State, receipt, before, after, r.physical.heldFences())
	}
}

func TestSQLiteAllocatedUnreleasedCompletionCensus(t *testing.T) {
	r := newUnreleasedSQLiteRig(t)
	r.physical.remote.onPut = func(*http.Request) {
		if err := os.WriteFile(filepath.Join(r.physical.path("-dir"), "unrelated"), []byte("changed"), 0600); err != nil {
			t.Error(err)
		}
	}
	op, receipt := r.prepare(t, "")
	if receipt != nil || op.State != ops.RecoveryRequired || r.physical.heldFences() != 1 {
		t.Fatal("completion accepted changed census", op.State, receipt)
	}
}

type interruptUnreleasedInit struct {
	datainit.Journal
	at string
}

func (j interruptUnreleasedInit) SetInitState(ctx context.Context, op datainit.Operation, state string) (datainit.Operation, error) {
	current, err := j.Journal.SetInitState(ctx, op, state)
	if err == nil && state == j.at {
		return current, datainit.ErrRecovery
	}
	return current, err
}

func (r *unreleasedSQLiteRig) interruptedInitialization(t *testing.T, at string) (datainit.Service, datainit.Plan, ops.Operation) {
	t.Helper()
	engine := r.initializer(t)
	p, err := engine.Plan(t.Context(), r.request)
	if err != nil {
		t.Fatal(err)
	}
	engine.Journal = interruptUnreleasedInit{Journal: engine.Journal, at: at}
	admission := initializationOperations{engine: engine, tasks: r.operations.jobs}
	accepted, err := admission.Apply(t.Context(), "hello", p.ID)
	if err != nil {
		t.Fatal(err)
	}
	runner := jobs.Runner{Store: r.deploy.store, TaskHandlers: map[ops.Kind]jobs.TaskHandler{ops.DataInitApply: func(ctx context.Context, op ops.Operation) (json.RawMessage, error) {
		_, err := engine.Apply(ctx, op.App, op.SecretRef)
		return nil, dispatch.DataInitializationFailure(err)
	}}}
	if err = runner.Run(t.Context(), accepted.OperationID); err == nil {
		t.Fatal("initialization interruption did not fail")
	}
	outer, err := r.deploy.store.GetOperation(t.Context(), accepted.OperationID)
	if err != nil || outer.State != ops.RecoveryRequired {
		t.Fatal("outer outcome", outer, err)
	}
	engine.Journal = r.deploy.store
	return engine, p, outer
}

func TestSQLiteUnreleasedSettledInitializationHistory(t *testing.T) {
	for _, at := range []string{"mutation_intent", "mutation_completed"} {
		t.Run(at, func(t *testing.T) { checkUnreleasedInitializationRecovery(t, at) })
	}
}

func checkUnreleasedInitializationRecovery(t *testing.T, at string) {
	t.Helper()
	r := newUnreleasedSQLiteRig(t)
	engine, p, outer := r.interruptedInitialization(t, at)
	settled, err := engine.Reconcile(t.Context(), "hello", p.ID)
	expected := "succeeded"
	if at == "mutation_intent" {
		expected = "not_initialized"
	}
	if err != nil || settled.State != expected {
		t.Fatal("reconcile", settled, err)
	}
	unchanged, err := r.deploy.store.GetOperation(t.Context(), outer.ID)
	if err != nil || unchanged.State != outer.State {
		t.Fatal("outer outcome rewritten", unchanged, err)
	}
	if at == "mutation_intent" {
		op, receipt := r.prepare(t, "")
		if receipt == nil {
			t.Fatal("settled empty preparation", op.State)
		}
		accepted, err := r.operations.Abandon(t.Context(), dispatch.SQLiteAbandonArgs{PreparationID: receipt.PreparationID, IdempotencyKey: r.physical.key()})
		if err != nil {
			t.Fatal(err)
		}
		if err = r.physical.runner(r.operations).Run(t.Context(), accepted.OperationID); err != nil {
			t.Fatal(err)
		}
		r.initialize(t)
	}
	op, receipt := r.prepare(t, "")
	if receipt == nil {
		t.Fatal("settled initialized preparation", op.State)
	}
}

type unreleasedRemoval struct {
	plan      archive.Plan
	operation archive.Operation
	task      ops.Operation
	guard     *store.SQLiteRemovalGuard
}

func (r *unreleasedSQLiteRig) consume(t *testing.T) unreleasedRemoval {
	t.Helper()
	state := r.deploy.store
	candidate, refs, err := state.RemovalCandidate(t.Context(), "hello")
	if err != nil || len(refs) != 1 {
		t.Fatal("removal candidate", candidate, refs, err)
	}
	retention, ok := r.deploy.policy.p.BackupRetention("primary")
	if !ok {
		t.Fatal("missing retention")
	}
	candidate.Databases[0] = candidate.Databases[0].WithRetention([]data.RetentionEvidence{retention})
	id, err := data.NewID()
	if err != nil {
		t.Fatal(err)
	}
	tree, err := data.CaptureArchiveTree(r.physical.database.Root, candidate.Incarnation, data.ArchiveID(id))
	if err != nil {
		t.Fatal(err)
	}
	hostRaw, _ := json.Marshal(r.deploy.inventory.snapshot.Identity)
	bounds, _ := r.deploy.policy.p.SQLiteRemovalBounds()
	p := archive.Plan{ArchiveID: data.ArchiveID(id), Requester: sqlitePrepRequester, PolicyHash: r.deploy.policy.p.Hash(), TargetHash: "sha256:" + sqliteHashForTest(hostRaw), Candidate: candidate, CreatedAt: time.Now().UTC(), Removal: &archive.Removal{Bounds: bounds, Points: refs, ExpiresAt: refs[0].ExpiresAt}, Trees: []data.ArchiveTree{tree}}
	p.ID, err = p.Fingerprint()
	if err != nil {
		t.Fatal(err)
	}
	if err = state.SaveArchivePlan(t.Context(), p); err != nil {
		t.Fatal("save removal", err)
	}
	input, err := state.SaveSQLiteRetireInput(t.Context(), store.SQLiteRetireInput{Kind: ops.SQLiteRetire, App: "hello", PlanID: p.ID})
	if err != nil {
		t.Fatal(err)
	}
	task, _, err := state.CreateOperation(t.Context(), ops.Intent{Kind: ops.SQLiteRetire, App: "hello", SecretRef: input}, sqlitePrepRequester, r.physical.key())
	if err != nil {
		t.Fatal(err)
	}
	if err = state.TransitionOperation(t.Context(), task.ID, ops.Queued, ops.Preflight); err != nil {
		t.Fatal(err)
	}
	guard, err := state.AcquireSQLiteRemovalGuard(t.Context(), []data.DatabaseID{r.physical.database.DatabaseID})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := guard.Close(); err != nil {
			t.Error(err)
		}
	})
	if err = guard.BindAuthority(r.deploy.policy.p, p.TargetHash); err != nil {
		t.Fatal(err)
	}
	child, err := guard.Child(r.physical.database.DatabaseID)
	if err != nil {
		t.Fatal(err)
	}
	hold, err := child.HoldSource(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	source, err := state.ReadSQLitePreparedSource(t.Context(), "hello", "main", refs[0].PreparationID)
	if err != nil {
		t.Fatal(err)
	}
	held, err := openSQLiteSource(r.physical.database, source.Definitions, source.Scope.EmptyAllocation, hold)
	if err != nil {
		_ = hold.Release()
		t.Fatal(err)
	}
	if err = guard.AttachSource(t.Context(), r.physical.database.DatabaseID, held); err != nil {
		_ = held.Close()
		t.Fatal(err)
	}
	operation, err := state.ClaimRemoval(t.Context(), p, task.ID, guard)
	if err != nil {
		t.Fatal("claim producing selectors", err)
	}
	return unreleasedRemoval{plan: p, operation: operation, task: task, guard: guard}
}

func TestSQLiteUnreleasedU1ConsumerAndHistoricalRead(t *testing.T) {
	for _, initialized := range []bool{false, true} {
		t.Run(fmt.Sprint(initialized), func(t *testing.T) { checkUnreleasedConsumer(t, initialized) })
	}
}

func checkUnreleasedConsumer(t *testing.T, initialized bool) {
	t.Helper()
	r := newUnreleasedSQLiteRig(t)
	if initialized {
		r.initialize(t)
	}
	_, receipt := r.prepare(t, "")
	if receipt == nil {
		t.Fatal("missing genuine preparation")
	}
	removal := r.consume(t)
	state := r.deploy.store
	if _, err := state.ReadSQLitePreparedSource(t.Context(), "hello", "main", receipt.PreparationID); err != nil {
		t.Fatal("consumed source read", err)
	}
	old, err := state.AdvanceArchiveDisposition(t.Context(), removal.operation, archive.Disposition{}, archive.Disposition{ArchiveID: removal.plan.ArchiveID, Sequence: 1, Stage: archive.AbandonIntent})
	if err != nil {
		t.Fatal(err)
	}
	_, result, err := state.FinishRemoval(t.Context(), removal.task.ID, removal.operation, old, archive.Disposition{ArchiveID: removal.plan.ArchiveID, Sequence: 2, Stage: archive.Abandoned}, removal.guard)
	if err != nil || result.Outcome != "abandoned" {
		t.Fatal("consumer completion", result, err)
	}
	if _, err = state.ReadSQLitePreparedSource(t.Context(), "hello", "main", receipt.PreparationID); err != nil {
		t.Fatal("terminal consumed source read", err)
	}
	if r.physical.heldFences() != 0 {
		t.Fatal("archive abandon retained original fence")
	}
}

func TestSQLiteInitializedUnreleasedRetiredAndRecreatedRead(t *testing.T) {
	r := newUnreleasedSQLiteRig(t)
	r.initialize(t)
	_, receipt := r.prepare(t, "")
	if receipt == nil {
		t.Fatal("missing preparation")
	}
	removal := r.consume(t)
	state := r.deploy.store
	operation := removal.operation
	var err error
	for _, stage := range []archive.Stage{archive.WritersStopIntent, archive.WritersStopped, archive.CoverageIntent} {
		operation, err = state.AdvanceArchive(t.Context(), operation, stage)
		if err != nil {
			t.Fatal(err)
		}
	}
	point, claimed, err := state.ReadSQLiteRemovalPoint(t.Context(), removal.plan.ID, r.physical.database.DatabaseID)
	if err != nil {
		t.Fatal(err)
	}
	coverage := archive.SQLiteCoverage{DatabaseID: r.physical.database.DatabaseID, WritersQuiescedAt: point.Source.Cutoff, Observation: point.Source.Point.Schema, Point: &point.Source.Point, Receipt: &point.Verification, Prepared: &archive.PreparedCoverage{Ref: removal.plan.Removal.Points[0], ClaimedAt: claimed}}
	operation, err = state.SaveArchiveCoverage(t.Context(), operation, []archive.Coverage{archive.SQLiteCoverageVariant(coverage)})
	if err != nil {
		t.Fatal(err)
	}
	for _, stage := range []archive.Stage{archive.ReplicaStopIntent, archive.Quiesced} {
		operation, err = state.AdvanceArchive(t.Context(), operation, stage)
		if err != nil {
			t.Fatal(err)
		}
	}
	operation, err = state.SaveArchiveManifest(t.Context(), operation, archive.Manifest{Plan: removal.plan, OperationID: operation.ID, Coverage: []archive.Coverage{archive.SQLiteCoverageVariant(coverage)}, QuiescedAt: time.Now().UTC(), PreparedAt: time.Now().UTC()})
	if err != nil {
		t.Fatal(err)
	}
	var disposition archive.Disposition
	for _, stage := range []archive.DispositionStage{archive.CleanupIntent, archive.RuntimeRetired, archive.RenameIntent, archive.Renamed} {
		disposition, err = state.AdvanceArchiveDisposition(t.Context(), operation, disposition, archive.Disposition{ArchiveID: removal.plan.ArchiveID, Sequence: disposition.Sequence + 1, Stage: stage})
		if err != nil {
			t.Fatal(err)
		}
	}
	tree := removal.plan.Trees[0]
	root := string(tree.Root)
	if err = os.MkdirAll(filepath.Join(root, "archives"), 0700); err != nil {
		t.Fatal(err)
	}
	if err = os.Rename(filepath.Join(root, tree.SourceRelative), filepath.Join(root, tree.ArchiveRelative)); err != nil {
		t.Fatal(err)
	}
	_, retired, err := state.FinishRemoval(t.Context(), removal.task.ID, operation, disposition, archive.Disposition{ArchiveID: removal.plan.ArchiveID, Sequence: disposition.Sequence + 1, Stage: archive.Retired}, removal.guard)
	if err != nil || retired.Outcome != "retired" {
		t.Fatal("retire", retired, err)
	}
	if err = removal.guard.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err = state.ReadSQLitePreparedSource(t.Context(), "hello", "main", receipt.PreparationID); err != nil {
		t.Fatal("retired source read", err)
	}
	app := initializedApp(t, r.deploy, r.request)
	planned, err := r.deploy.service.PlanDataPreparation(t.Context(), app)
	if err != nil {
		t.Fatal("recreate plan", err)
	}
	accepted := r.deploy.call(t, "apply", dispatch.ApplyArgs{PlanID: planned.PlanID, IdempotencyKey: "recreated-unreleased"}).Data.(jobs.Accepted)
	if err = r.deploy.runner.Run(t.Context(), accepted.OperationID); err != nil {
		t.Fatal("recreate", err)
	}
	incarnation, err := state.ActiveDataIncarnation(t.Context(), "hello")
	if err != nil || incarnation == r.physical.database.IncarnationID {
		t.Fatal("recreation reused identity", incarnation, err)
	}
	if _, err = state.ReadSQLitePreparedSource(t.Context(), "hello", "main", receipt.PreparationID); err != nil {
		t.Fatal("recreated source history read", err)
	}
	task, _, err := state.SQLiteTask(t.Context(), receipt.Task.OperationID)
	if err != nil {
		t.Fatal(err)
	}
	status, err := state.SQLitePreparationJournal(nil).ReadTask(t.Context(), task)
	if err != nil || status.State != sqliteprep.Succeeded {
		t.Fatal("historical success read", status, err)
	}
}

func retainUnreleasedForeignLifetime(t *testing.T, r *unreleasedSQLiteRig) {
	t.Helper()
	if err := ensurePrivateChild(r.physical.root, "replica-locks"); err != nil {
		t.Fatal(err)
	}
	lock, err := replication.AcquireLifetimeLock(t.Context(), filepath.Join(r.physical.root, "replica-locks", string(r.physical.replica.BindingID)+".lock"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := lock.Release(); err != nil {
			t.Error(err)
		}
	})
}

type unreleasedCompletionJournal struct {
	sqliteprep.Journal
	mode string
	path string
}

func (j unreleasedCompletionJournal) CompletePrepare(ctx context.Context, h sqliteprep.History, p sqliteprep.PreparedPoint, held sqliteprep.StoppedSource) (sqliteprep.Receipt, error) {
	if j.mode == "precommit-entry" {
		held = &unreleasedCompletionSource{StoppedSource: held, path: j.path}
	}
	receipt, err := j.Journal.CompletePrepare(ctx, h, p, held)
	if err == nil && j.mode == "lost-reply" {
		return sqliteprep.Receipt{}, errors.New("fixture lost completion acknowledgement")
	}
	return receipt, err
}

type unreleasedCompletionSource struct {
	sqliteprep.StoppedSource
	path   string
	checks int
}

func (s *unreleasedCompletionSource) SQLiteSourceHold() *store.SQLiteSourceHold {
	return s.StoppedSource.(*sqliteSource).SQLiteSourceHold()
}
func (s *unreleasedCompletionSource) Recheck(ctx context.Context) (sqliteprep.StoppedBundle, error) {
	s.checks++
	if s.checks == 2 {
		if err := os.WriteFile(s.path, []byte("precommit race"), 0600); err != nil {
			return sqliteprep.StoppedBundle{}, err
		}
	}
	return s.StoppedSource.Recheck(ctx)
}

func TestSQLiteAllocatedUnreleasedAtomicCompletion(t *testing.T) {
	for _, mode := range []string{"precommit-entry", "lost-reply"} {
		t.Run(mode, func(t *testing.T) { checkUnreleasedAtomicCompletion(t, mode) })
	}
}

func checkUnreleasedAtomicCompletion(t *testing.T, mode string) {
	t.Helper()
	r := newUnreleasedSQLiteRig(t)
	accepted, err := r.operations.Prepare(t.Context(), dispatch.SQLitePrepareArgs{App: "hello", Database: "main", DeclarationPlanID: r.plan, DeliveredCredentialVersion: r.credential.Version, IdempotencyKey: r.physical.key()})
	if err != nil {
		t.Fatal(err)
	}
	runner := jobs.Runner{Store: r.deploy.store, TaskHandlers: map[ops.Kind]jobs.TaskHandler{ops.SQLitePrepare: func(ctx context.Context, op ops.Operation) (json.RawMessage, error) {
		task, input, err := r.deploy.store.SQLiteTask(ctx, op.ID)
		if err != nil {
			return nil, err
		}
		journal := unreleasedCompletionJournal{Journal: r.deploy.store.SQLiteObservedPreparationJournal(observeSQLiteLocation, observeSQLiteSchema), mode: mode, path: filepath.Join(r.physical.path("-dir"), "precommit-entry")}
		result, err := (sqliteprep.Service{Journal: journal, Host: r.operations.host}).Prepare(ctx, task, input)
		if err != nil {
			return nil, err
		}
		return sqliteprep.Encode(result.Receipt)
	}}}
	runErr := runner.Run(t.Context(), accepted.OperationID)
	op, receipt := r.physical.outcome(accepted.OperationID)
	if mode == "precommit-entry" {
		if runErr == nil || receipt != nil || op.State != ops.RecoveryRequired {
			t.Fatal("precommit census raced through commit", op.State, runErr)
		}
		return
	}
	if runErr != nil || receipt == nil {
		t.Fatal("lost acknowledgement did not read joined success", op.State, runErr)
	}
	task, input, err := r.deploy.store.SQLiteTask(t.Context(), accepted.OperationID)
	if err != nil {
		t.Fatal(err)
	}
	replay, err := (sqliteprep.Service{Journal: r.deploy.store.SQLiteObservedPreparationJournal(observeSQLiteLocation, observeSQLiteSchema), Host: r.operations.host}).Prepare(t.Context(), task, input)
	if err != nil || replay.Receipt != *receipt {
		t.Fatal("joined replay", replay, err)
	}
	puts, gets := r.physical.remote.counts()
	if puts != 2 || gets != 1 {
		t.Fatal("readback repeated effects", puts, gets)
	}
}

type changedUnreleasedPolicy struct{}

func (changedUnreleasedPolicy) Load(ctx context.Context) (policy.Policy, error) {
	if err := ctx.Err(); err != nil {
		return policy.Policy{}, err
	}
	raw, err := os.ReadFile("../policy/testdata/operator.toml")
	if err != nil {
		return policy.Policy{}, err
	}
	return policy.Parse(raw)
}

func TestSQLiteInitializedUnreleasedProspectivePlanStillRefusesUnactivatedCredential(t *testing.T) {
	r := newUnreleasedSQLiteRig(t)
	r.initialize(t)
	app := initializedApp(t, r.deploy, r.request)
	_, desired, err := r.deploy.store.LoadPlan(t.Context(), r.plan)
	if err != nil {
		t.Fatal(err)
	}
	app.RestoreInvariants = desired.RestoreInvariants
	planned, err := r.deploy.service.Plan(t.Context(), app)
	if err != nil || planned.Kind != "conflict" {
		t.Fatal("ordinary prospective plan gate changed", planned, err)
	}
	found := false
	for _, conflict := range planned.Conflicts {
		if string(conflict.Code) == "backup_credential_unknown" {
			found = true
		}
	}
	if !found {
		t.Fatal("unexpected ordinary planning refusal", planned.Conflicts)
	}
	op, receipt := r.prepare(t, "")
	if receipt == nil {
		t.Fatal("prospective declaration refused", op.State)
	}
}

func unreleasedSchemaFingerprint(t *testing.T, root string) string {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+filepath.Join(root, "control.db")+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := db.Close(); err != nil {
			t.Error(err)
		}
	}()
	var version int
	if err = db.QueryRowContext(t.Context(), "SELECT version FROM schema_version").Scan(&version); err != nil || version != 37 {
		t.Fatal("schema version changed", version, err)
	}
	rows, err := db.QueryContext(t.Context(), "SELECT type,name,tbl_name,COALESCE(sql,'') FROM sqlite_master ORDER BY type,name")
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := rows.Close(); err != nil {
			t.Error(err)
		}
	}()
	objects := [][4]string{}
	for rows.Next() {
		var object [4]string
		if err = rows.Scan(&object[0], &object[1], &object[2], &object[3]); err != nil {
			t.Fatal(err)
		}
		objects = append(objects, object)
	}
	if err = rows.Err(); err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(objects)
	if err != nil {
		t.Fatal(err)
	}
	return sqliteHashForTest(raw)
}
