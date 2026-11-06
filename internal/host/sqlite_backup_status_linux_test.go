//go:build linux

package host

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/dispatch"
	"github.com/ShaulLavo/brine/internal/localexec"
	"github.com/ShaulLavo/brine/internal/policy"
	"github.com/ShaulLavo/brine/internal/restore"
	"github.com/ShaulLavo/brine/internal/sqlitestatus"
	"github.com/ShaulLavo/brine/internal/store"
)

type sqliteStatusExecutor struct{}

func (e *sqliteStatusExecutor) Execute(_ context.Context, c localexec.Command) (localexec.Result, error) {
	if c.Mutation || c.Path != "systemctl" || !strings.Contains(strings.Join(c.Args, " "), "--user show brine-litestream-") {
		return localexec.Result{}, errors.New("PLANTED unexpected mutation")
	}
	return localexec.Result{Stdout: "LoadState=loaded\nActiveState=active\nSubState=running\nNRestarts=0\nActiveEnterTimestampMonotonic=1\n"}, nil
}

type sqliteStatusCLI struct {
	mode    string
	calls   int
	entered chan struct{}
}

func (c *sqliteStatusCLI) Execute(ctx context.Context, cmd restore.Command) (restore.CommandResult, error) {
	c.calls++
	if len(cmd.Args) == 1 {
		return restore.CommandResult{Stdout: []byte(restore.LitestreamVersion)}, nil
	}
	if err := os.WriteFile(filepath.Join(cmd.Directory, "partial.sqlite"), []byte("unexpected partial dry-run output"), 0600); err != nil {
		return restore.CommandResult{}, err
	}
	if c.mode == "cancel" {
		close(c.entered)
	}
	if c.mode == "timeout" || c.mode == "cancel" {
		<-ctx.Done()
		return restore.CommandResult{}, ctx.Err()
	}
	if c.mode == "invalid" {
		return restore.CommandResult{Stdout: []byte(`{"secret":"PLANTED_PROVIDER_MESSAGE"}`)}, nil
	}
	output := ""
	for i, arg := range cmd.Args {
		if arg == "-o" {
			output = cmd.Args[i+1]
		}
	}
	raw, _ := json.Marshal(map[string]string{"target_path": output, "replica": "s3", "max_txid": "0000000000000007"})
	return restore.CommandResult{Stdout: raw}, nil
}
func sqliteStatusRig(t *testing.T, expiry *time.Time, credential bool) (*ServerFactory, *sqliteBackupStatusReader, *sqliteStatusCLI, string) {
	t.Helper()
	root := t.TempDir()
	if err := os.Chmod(root, 0700); err != nil { // #nosec G302 -- Private fixture directory requires traversal.
		t.Fatal(err)
	}
	state, err := store.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = state.Close() })
	request := store.DataReservation{App: "example", PolicyHash: "sha256:" + strings.Repeat("a", 64), Database: data.SQLiteDatabase{Name: "main", PersistentRoot: "/never-open-source", MountPath: "/data", Filename: "app.db", BackupDestination: "primary"}, Destination: data.Destination{Reference: "primary", Endpoint: "https://storage.invalid", Region: "region-1", Bucket: "backups", BasePrefix: "brine", CredentialRef: "primary"}}
	reserved, err := state.ReserveDatabase(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	b := reserved.Replica
	b.ConfigContent = "dbs: []\n"
	sum := sha256.Sum256([]byte(b.ConfigContent))
	b.ConfigSHA256 = hex.EncodeToString(sum[:])
	b.UnitSHA256 = strings.Repeat("c", 64)
	b.CredentialVersion = 1
	b.ConfigFile = filepath.Join(root, "replication", string(b.BindingID), "configs", b.ConfigSHA256+".yml")
	b.SocketFile = filepath.Join(root, "replication", string(b.BindingID), "control.sock")
	b.LifetimeLockFile = filepath.Join(root, "replica-locks", string(b.BindingID)+".lock")
	b.CredentialFile = filepath.Join(root, "credentials/s3/primary/v1.env")
	if err := state.CommitReplicaBinding(t.Context(), b); err != nil {
		t.Fatal(err)
	}
	if credential {
		sqliteStatusWriteCredentials(t, state, root, b, expiry)
	}
	factory := newServerFactory("fixture", "deploy", func(context.Context, string) (*Runtime, error) {
		t.Fatal("status opened mutation runtime")
		return nil, nil
	}, func(context.Context) (dispatch.Inventory, error) { t.Fatal("status opened inventory"); return nil, nil })
	factory.diagnosticStateDir = root
	server, err := factory.Build(t.Context(), "sqlite_backup_status")
	if err != nil {
		t.Fatal(err)
	}
	reader, ok := server.SQLiteBackupStatus.(*sqliteBackupStatusReader)
	if !ok {
		t.Fatal("missing lightweight reader")
	}
	cli := &sqliteStatusCLI{}
	reader.policy = &fakePolicy{err: errors.New("PLANTED_PRIVATE_POLICY")}
	reader.executor = &sqliteStatusExecutor{}
	reader.cli = cli
	reader.listingBudget = 20 * time.Millisecond
	reader.timeBudget = time.Second
	reader.probe = func(_ context.Context, _ restore.Destination, _ restore.Credentials) restore.RemoteProbe {
		return restore.RemoteProbe{State: restore.RemoteNetworkUnavailable, ObservedAt: time.Now().UTC()}
	}
	return factory, reader, cli, root
}
func sqliteStatusWriteCredentials(t *testing.T, state *store.Store, root string, b data.ReplicaBinding, expiry *time.Time) {
	t.Helper()
	hash := "sha256:" + strings.Repeat("a", 64)
	record := store.CredentialRecord{App: "example", Requester: "operator", ID: strings.Repeat("d", 32), Kind: "receipt", PlanID: hash, PlanHash: hash, TargetHash: hash, PolicyHash: hash, BindingID: b.BindingID, EpochID: b.EpochID, Destination: b.Destination.Reference, CredentialRef: b.Destination.CredentialRef, Version: 1, ReceivedAt: time.Now().UTC().Add(-time.Hour), ExpiresAt: expiry}
	if err := state.SaveCredentialRecord(t.Context(), record); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "credentials/s3/primary"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "credentials/s3/primary/v1.env"), []byte("AWS_ACCESS_KEY_ID=PLANTED_ACCESS\nAWS_SECRET_ACCESS_KEY=PLANTED_SECRET\n"), 0600); err != nil {
		t.Fatal(err)
	}
}
func sqliteStatusDispatch(t *testing.T, ctx context.Context, f *ServerFactory, r *sqliteBackupStatusReader) (sqlitestatus.Status, error) {
	t.Helper()
	server := dispatch.NewServer("fixture", nil)
	server.Factory = func(ctx context.Context, op string) (*dispatch.Server, error) {
		s, err := f.Build(ctx, op)
		if err == nil {
			s.SQLiteBackupStatus = r
		}
		return s, err
	}
	wire, err := dispatch.EncodeRequest(dispatch.Request{SchemaVersion: 1, Op: "sqlite_backup_status", RequestID: "status", Args: json.RawMessage(`{"app":"example","database":"main"}`)})
	if err != nil {
		t.Fatal(err)
	}
	response, err := server.Handle(ctx, bytes.NewReader(wire))
	if err != nil {
		return sqlitestatus.Status{}, err
	}
	raw, err := json.Marshal(response)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "PLANTED") {
		t.Fatal("private messages or credentials leaked")
	}
	decoded, err := dispatch.DecodeResponse(raw, "sqlite_backup_status")
	if err != nil {
		t.Fatal(err)
	}
	return decoded.Data.(sqlitestatus.Status), nil
}
func TestSQLiteStatusFactoryActiveButUnreachableAndDryRunDisposal(t *testing.T) {
	for _, mode := range []string{"known", "invalid", "timeout", "cancel"} {
		t.Run(mode, func(t *testing.T) {
			sqliteStatusDryRunCase(t, mode)
		})
	}
}
func sqliteStatusDryRunCase(t *testing.T, mode string) {
	t.Helper()
	f, r, cli, root := sqliteStatusRig(t, nil, true)
	cli.mode = mode
	if mode == "timeout" {
		r.timeBudget = 30 * time.Millisecond
	}
	ctx := t.Context()
	cancel := func() {}
	if mode == "cancel" {
		ctx, cancel = sqliteStatusCancelDuringDryRun(t, cli)
	}
	defer cancel()
	status, err := sqliteStatusDispatch(t, ctx, f, r)
	if mode == "cancel" {
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("caller cancellation lost: %v", err)
		}
	} else if err != nil || status.State != "degraded" || status.Service.State != "active" || status.Remote.State != restore.RemoteNetworkUnavailable || status.LocalPosition != nil || status.Loss != "unknown" {
		t.Fatalf("untruthful live facts: %+v %v", status, err)
	}
	if mode == "known" && (status.Watermark.State != "known" || status.Watermark.TXID == nil || *status.Watermark.TXID != 7) {
		t.Fatal("positive watermark missing")
	}
	if mode == "invalid" && status.Watermark.State != "failed" {
		t.Fatal("parser failure not typed")
	}
	if mode == "timeout" && status.Watermark.State != "timed_out" {
		t.Fatal("child timeout killed complete status")
	}
	assertNoSQLiteStatusScratch(t, root)
}

func assertNoSQLiteStatusScratch(t *testing.T, root string) {
	t.Helper()
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), "sqlite-status-") {
			t.Fatal("status retained scratch parent", entry.Name())
		}
	}
}
func TestSQLiteStatusSkipsAbsentExpiredOrUnreadableCredentialWithoutCreatingDirectories(t *testing.T) {
	for _, kind := range []string{"absent", "expired", "unreadable"} {
		t.Run(kind, func(t *testing.T) {
			sqliteStatusCredentialCase(t, kind)
		})
	}
}
func sqliteStatusCredentialCase(t *testing.T, kind string) {
	t.Helper()
	expired := time.Now().UTC().Add(-time.Minute)
	var expiry *time.Time
	if kind == "expired" {
		expiry = &expired
	}
	f, r, cli, root := sqliteStatusRig(t, expiry, kind != "absent")
	if kind == "unreadable" {
		if err := os.RemoveAll(filepath.Join(root, "credentials")); err != nil {
			t.Fatal(err)
		}
	}
	r.probe = func(context.Context, restore.Destination, restore.Credentials) restore.RemoteProbe {
		t.Fatal("remote request with unusable credentials")
		return restore.RemoteProbe{}
	}
	status, err := sqliteStatusDispatch(t, t.Context(), f, r)
	if err != nil || cli.calls != 0 || status.Remote.State != restore.RemoteNotChecked || status.Watermark.State != "not_checked" {
		t.Fatal("credential skip failed", err)
	}
	if kind != "expired" {
		if _, err := os.Stat(filepath.Join(root, "credentials")); !os.IsNotExist(err) {
			t.Fatal("status created missing credentials")
		}
	}
	assertNoSQLiteStatusScratch(t, root)
}

func TestSQLiteStatusRealFactoryRefusesOldStoreWithoutMigration(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil { // #nosec G302 -- Private fixture directory requires traversal.
		t.Fatal(err)
	}
	path := filepath.Join(dir, "control.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("CREATE TABLE schema_version(version INTEGER); INSERT INTO schema_version VALUES(33)"); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0600); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(path) // #nosec G304 -- Exact test-created control database in a private fixture.
	if err != nil {
		t.Fatal(err)
	}
	f := newServerFactory("fixture", "deploy", func(context.Context, string) (*Runtime, error) { t.Fatal("old store opened runtime"); return nil, nil }, nil)
	f.diagnosticStateDir = dir
	server := dispatch.NewServer("fixture", nil)
	server.Factory = f.Build
	raw := []byte(`{"schema_version":1,"op":"sqlite_backup_status","request_id":"old","args":{"app":"example","database":"main"}}`)
	response, err := server.Handle(t.Context(), bytes.NewReader(raw))
	if err == nil || response.OK {
		t.Fatal("old schema accepted")
	}
	after, err := os.ReadFile(path) // #nosec G304 -- Exact test-created control database in a private fixture.
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("old store migrated or changed", err)
	}
	assertNoSQLiteStatusScratch(t, dir)
}

func TestSQLiteStatusSlowListingIsPartialBeforeOverallDeadline(t *testing.T) {
	f, r, _, root := sqliteStatusRig(t, nil, true)
	r.probe = func(ctx context.Context, _ restore.Destination, _ restore.Credentials) restore.RemoteProbe {
		<-ctx.Done()
		return restore.RemoteProbe{State: restore.RemoteNetworkUnavailable, ObservedAt: time.Now().UTC()}
	}
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	status, err := sqliteStatusDispatch(t, ctx, f, r)
	if err != nil || ctx.Err() != nil || status.Remote.State != restore.RemoteNetworkUnavailable || status.Watermark.State != "known" {
		t.Fatal("slow listing lost partial output", err)
	}
	assertNoSQLiteStatusScratch(t, root)
}
func TestSQLiteStatusWillNotSubstituteAnotherCredentialVersion(t *testing.T) {
	f, r, cli, root := sqliteStatusRig(t, nil, true)
	if err := os.Rename(filepath.Join(root, "credentials/s3/primary/v1.env"), filepath.Join(root, "credentials/s3/primary/v2.env")); err != nil {
		t.Fatal(err)
	}
	r.probe = func(context.Context, restore.Destination, restore.Credentials) restore.RemoteProbe {
		t.Fatal("substituted newer credential")
		return restore.RemoteProbe{}
	}
	status, err := sqliteStatusDispatch(t, t.Context(), f, r)
	if err != nil || status.Credential.Version != 1 || status.Credential.State != "unreadable" || cli.calls != 0 {
		t.Fatal("exact delivered version was not preserved", err)
	}
}
func TestSQLiteStatusParentDisposalRefusesForeignOrNonemptyDirectory(t *testing.T) {
	dir := t.TempDir()
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = root.Close() }()
	if err := root.Mkdir("parent", 0700); err != nil {
		t.Fatal(err)
	}
	original, err := root.Lstat("parent")
	if err != nil {
		t.Fatal(err)
	}
	if err := root.Rename("parent", "saved"); err != nil {
		t.Fatal(err)
	}
	if err := root.Mkdir("parent", 0700); err != nil {
		t.Fatal(err)
	}
	if err := removeSQLiteStatusParent(root, "parent", original); err == nil {
		t.Fatal("removed substituted parent")
	}
	if err := root.WriteFile("saved/keep", []byte("owned test content"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := removeSQLiteStatusParent(root, "saved", original); err == nil {
		t.Fatal("recursively deleted a nonempty parent")
	}
	if _, err := root.Stat("saved/keep"); err != nil {
		t.Fatal("lost retained file", err)
	}
}

func sqliteStatusCancelDuringDryRun(t *testing.T, cli *sqliteStatusCLI) (context.Context, context.CancelFunc) {
	t.Helper()
	parent, stop := context.WithTimeout(t.Context(), 5*time.Second)
	ctx, cancel := context.WithCancel(parent)
	cli.entered = make(chan struct{})
	done := make(chan struct{})
	t.Cleanup(func() { cancel(); stop(); <-done })
	go func() {
		defer close(done)
		select {
		case <-cli.entered:
			cancel()
		case <-ctx.Done():
		}
	}()
	return ctx, cancel
}

func TestSQLiteStatusLifecycleFreshAndStaleControls(t *testing.T) {
	for _, state := range []string{"fresh", "stale"} {
		t.Run(state, func(t *testing.T) { sqliteStatusLifecycleCase(t, state) })
	}
}
func sqliteStatusLifecycleCase(t *testing.T, want string) {
	t.Helper()
	f, r, _, root := sqliteStatusRig(t, nil, true)
	at := time.Now().UTC().Add(-time.Minute)
	if want == "stale" {
		at = at.Add(-2 * time.Hour)
	}
	raw := fmt.Sprintf(`schema_version=1
version="status-fixture"
[resources]
memory_mb=512
pids_limit=128
[[backup_destinations]]
reference="primary"
endpoint="https://storage.invalid"
region="region-1"
bucket="backups"
base_prefix="brine"
credential_ref="primary"
[[backup_retention]]
destination="primary"
endpoint="https://storage.invalid"
bucket="backups"
base_prefix="brine"
verification_id="11111111111111111111111111111111"
verified_at=%q
freshness_seconds=3600
no_object_expiration=true
`, at.Format(time.RFC3339Nano))
	pol, err := policy.Parse([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	r.policy = &fakePolicy{p: pol}
	r.probe = func(context.Context, restore.Destination, restore.Credentials) restore.RemoteProbe {
		return restore.RemoteProbe{State: restore.RemoteReachable, ObservedAt: time.Now().UTC()}
	}
	status, err := sqliteStatusDispatch(t, t.Context(), f, r)
	if err != nil || status.Lifecycle.State != want || status.Lifecycle.VerifiedAt == nil || status.Lifecycle.FreshUntil == nil {
		t.Fatal("lifecycle evidence not observed", err)
	}
	expected := "unverified"
	if want == "stale" {
		expected = "degraded"
	}
	if status.State != expected || status.LastVerified != nil {
		t.Fatalf("remote watermark promoted to durability: %+v", status)
	}
	assertNoSQLiteStatusScratch(t, root)
}
