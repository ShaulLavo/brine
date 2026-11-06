//go:build linux

package host

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
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
	"github.com/ShaulLavo/brine/internal/quadlet"
	"github.com/ShaulLavo/brine/internal/replication"
	"github.com/ShaulLavo/brine/internal/sqliteprep"
)

type reviewU4Executor func(context.Context, localexec.Command) (localexec.Result, error)

func (e reviewU4Executor) Execute(ctx context.Context, c localexec.Command) (localexec.Result, error) {
	return e(ctx, c)
}

func reviewU4Rig(t *testing.T) *sqlitePrepRig {
	t.Helper()
	r := newSQLitePrepRig(t, []string{"v2"}, "v2")
	for _, path := range []string{r.root, r.home, r.proc, string(r.database.Root)} {
		t.Logf("OWNED_PATH %s", path)
	}
	return r
}

func reviewU4ReplaceRoot(r *sqlitePrepRig, moved string) error {
	if err := os.Rename(string(r.database.Root), moved); err != nil {
		return err
	}
	if err := os.MkdirAll(r.path("-dir"), 0700); err != nil {
		return err
	}
	if err := os.WriteFile(r.path(""), r.source, 0600); err != nil {
		return err
	}
	db, err := sql.Open("sqlite", r.path(""))
	if err != nil {
		return err
	}
	_, err = db.Exec("INSERT INTO items(name) VALUES ('replacement-root-data')")
	return errors.Join(err, db.Close())
}

func TestReviewU4RootNameMustStillReachHeldSource(t *testing.T) {
	r := reviewU4Rig(t)
	root := string(r.database.Root)
	moved := root + ".review-held"
	t.Logf("OWNED_PATH %s", moved)
	t.Cleanup(func() { _ = os.RemoveAll(moved) })
	var once sync.Once
	changed := make(chan error, 1)
	r.remote.onPut = func(*http.Request) {
		once.Do(func() { changed <- reviewU4ReplaceRoot(r, moved) })
	}
	op, receipt := r.prepare(context.Background(), "")
	if err := <-changed; err != nil {
		t.Fatal("root replacement fixture failed", err)
	}
	r.exec.assertNothingStarted(t)
	if receipt != nil || op.State != ops.RecoveryRequired || r.heldFences() != 1 {
		t.Fatalf("replaced absolute persistent root completed against detached old root: state=%s receipt=%+v", op.State, receipt)
	}
}

// A barrier outage is a local fake failure after a real claim and writer stop.
// No remote write, expiry injection or store-row mutation is needed.
func TestReviewU4LiveFailureHasCredentialIndependentAbandon(t *testing.T) {
	r := reviewU4Rig(t)
	r.startSupervisor()
	o := r.operations()
	o.host.executor = reviewU4Executor(func(ctx context.Context, c localexec.Command) (localexec.Result, error) {
		if c.Path == replication.Executable {
			return localexec.Result{ExitCode: 1}, errors.New("fixture barrier unavailable")
		}
		return r.exec.Execute(ctx, c)
	})
	a, err := o.Prepare(context.Background(), dispatch.SQLitePrepareArgs{App: "hello", Database: "main", IdempotencyKey: r.key()})
	if err != nil {
		t.Fatal(err)
	}
	_ = r.runner(o).Run(context.Background(), a.OperationID)
	op, receipt := r.outcome(a.OperationID)
	if receipt != nil || op.State != ops.RecoveryRequired || r.heldFences() != 1 || r.exec.count("stop hello.service") != 1 || r.exec.count("stop "+r.replicaUnit()) != 0 {
		t.Fatal("fault did not reach claimed live recovery", op.State)
	}
	// Losing the packet makes supersede's required verification unavailable.
	if err = os.Remove(filepath.Join(r.root, "credentials/s3/primary/v1.env")); err != nil {
		t.Fatal(err)
	}
	abandoned, abandon := r.abandon(r.preparationID(op))
	r.exec.assertNothingStarted(t)
	if abandon == nil || r.heldFences() != 0 {
		t.Fatalf("safe local live-failure abandon stranded: task=%s fence_count=%d replica_stop_count=%d", abandoned.State, r.heldFences(), r.exec.count("stop "+r.replicaUnit()))
	}
}

func reviewU4RunPoint(r *sqlitePrepRig, point, database string) ops.State {
	r.t.Helper()
	return reviewU4RunRestore(r, dispatch.RestoreTestArgs{App: "hello", Database: database, Point: point})
}

func reviewU4RunRestore(r *sqlitePrepRig, args dispatch.RestoreTestArgs) ops.State {
	r.t.Helper()
	ref, err := r.state.SaveRestoreTaskInput(context.Background(), sqlitePrepRequester, args)
	if err != nil {
		r.t.Fatal(err)
	}
	service := jobs.Service{Store: r.state, Requester: sqlitePrepRequester, Launcher: credentialTestLauncher{}}
	accepted, err := service.Submit(context.Background(), ops.Intent{Kind: ops.RestoreTest, App: "hello", SecretRef: ref}, r.key())
	if err != nil {
		r.t.Fatal(err)
	}
	tests := restoreTests{stateDir: r.root, cli: r.host().cli, endpoint: r.remote.server.URL, allowHTTP: true}
	runner := jobs.Runner{Store: r.state, TaskHandlers: map[ops.Kind]jobs.TaskHandler{ops.RestoreTest: restoreTaskHandlerWithCheck(r.state, tests.Check)}}
	_ = runner.Run(context.Background(), accepted.OperationID)
	status, err := service.Operation(context.Background(), accepted.OperationID, 0)
	if err != nil {
		r.t.Fatal(err)
	}
	if status.Operation.State == ops.Succeeded {
		var receipt map[string]any
		if json.Unmarshal(status.Outcome.Receipt, &receipt) != nil {
			r.t.Fatal("invalid restore receipt")
		}
	}
	return status.Operation.State
}

func TestReviewU4PointTaskRefusesMissingExactPacketWithoutEffects(t *testing.T) {
	r := reviewU4Rig(t)
	_, point := r.prepare(context.Background(), "")
	if point == nil {
		t.Fatal("prepare failed")
	}
	if err := os.Remove(filepath.Join(r.root, "credentials/s3/primary/v1.env")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(r.root, "credentials/s3/primary/v2.env"), []byte("AWS_ACCESS_KEY_ID=other\nAWS_SECRET_ACCESS_KEY=other\n"), 0600); err != nil {
		t.Fatal(err)
	}
	puts, gets := r.remote.counts()
	stops := r.exec.count(" stop ")
	if state := reviewU4RunPoint(r, point.PreparationID, "main"); state != ops.Failed {
		t.Fatal("missing exact delivered version did not fail", state)
	}
	afterPuts, afterGets := r.remote.counts()
	if puts != afterPuts || gets != afterGets || r.exec.count(" stop ") != stops || r.heldFences() != 1 {
		t.Fatal("point refusal had host or remote effects")
	}
	if _, abandoned := r.abandon(point.PreparationID); abandoned == nil {
		t.Fatal("stopped abandon incorrectly required remote packet")
	}
	r.exec.assertNothingStarted(t)
}

func TestReviewU4ConsumedPointTaskRemainsReadOnlyHistoricalTest(t *testing.T) {
	r := reviewU4Rig(t)
	_, point := r.prepare(context.Background(), "")
	if point == nil {
		t.Fatal("prepare failed")
	}
	if _, abandoned := r.abandon(point.PreparationID); abandoned == nil {
		t.Fatal("abandon failed")
	}
	writeSQLitePrepDatabase(t, r.path(""), "v3")
	puts, _ := r.remote.counts()
	stops := r.exec.count(" stop ")
	if state := reviewU4RunPoint(r, point.PreparationID, "main"); state != ops.Succeeded {
		t.Fatal("ordinary historical point test was treated as data consumer", state)
	}
	afterPuts, _ := r.remote.counts()
	if puts != afterPuts || stops != r.exec.count(" stop ") || r.heldFences() != 0 {
		t.Fatal("historical point test mutated preparation or source")
	}
	r.exec.assertNothingStarted(t)
}

func TestReviewU4UnknownSecondPutCapturesNewSuccessorKey(t *testing.T) {
	r := reviewU4Rig(t)
	var calls int
	var mu sync.Mutex
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r.remote.onPut = func(*http.Request) {
		mu.Lock()
		calls++
		count := calls
		mu.Unlock()
		if count == 2 {
			cancel()
		}
	}
	op, receipt := r.prepare(ctx, "")
	if receipt != nil || op.State != ops.RecoveryRequired || r.heldFences() != 1 {
		t.Fatal("second-reply loss not uncertain", op.State)
	}
	r.remote.mu.Lock()
	r.remote.onPut = nil
	r.remote.mu.Unlock()
	oldID := r.preparationID(op)
	_, successor := r.prepare(context.Background(), oldID)
	if successor == nil || successor.SourcePointID == oldID {
		t.Fatal("unknown capability reply was adopted rather than recaptured")
	}
	r.remote.mu.Lock()
	keys := make([]string, 0, len(r.remote.objects))
	for key := range r.remote.objects {
		keys = append(keys, key)
	}
	r.remote.mu.Unlock()
	if len(keys) != 2 {
		t.Fatalf("expected old uncertain and fresh successor keys, got %v", keys)
	}
	r.exec.assertNothingStarted(t)
}

func TestReviewU4LostReplicaStopReplyCanAbandon(t *testing.T) {
	r := reviewU4Rig(t)
	r.startSupervisor()
	o := r.operations()
	executor := reviewU4Executor(func(ctx context.Context, c localexec.Command) (localexec.Result, error) {
		result, err := r.exec.Execute(ctx, c)
		if c.Path == "/usr/bin/systemctl" && strings.Join(c.Args, " ") == "--user stop "+r.replicaUnit() {
			return result, errors.New("fixture stop acknowledgement lost")
		}
		return result, err
	})
	session, err := localexec.NewSession(executor, nativeBackupFixtureUID(t), "/", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	o.host.session = session
	a, err := o.Prepare(context.Background(), dispatch.SQLitePrepareArgs{App: "hello", Database: "main", IdempotencyKey: r.key()})
	if err != nil {
		t.Fatal(err)
	}
	_ = r.runner(o).Run(context.Background(), a.OperationID)
	op, receipt := r.outcome(a.OperationID)
	if receipt != nil || op.State != ops.RecoveryRequired || r.heldFences() != 1 {
		t.Fatal("stop reply loss was replayed or completed", op.State)
	}
	if _, abandoned := r.abandon(r.preparationID(op)); abandoned == nil || r.heldFences() != 0 {
		t.Fatal("actually stopped live failure was stranded")
	}
	r.exec.assertNothingStarted(t)
}

func TestReviewU4ForeignWriterServiceMustNotBeStopped(t *testing.T) {
	r := reviewU4Rig(t)
	// A native user unit overrides the recorded app's generated Quadlet service.
	// The real fixture current release remains unchanged. Only systemd and its
	// process evidence are faked, as in the submitted rig.
	unit := filepath.Join(r.home, ".config/systemd/user/hello.service")
	if err := os.WriteFile(unit, []byte("[Unit]\nDescription=Unrelated fixture service\n[Service]\nExecStart=/usr/bin/sleep infinity\n"), 0600); err != nil {
		t.Fatal(err)
	}
	op, receipt := r.prepare(context.Background(), "")
	if receipt != nil || op.State != ops.Failed || r.exec.count("stop hello.service") != 0 || r.heldFences() != 0 {
		t.Fatalf("loaded name was treated as writer ownership: state=%s writer_stops=%d fences=%d", op.State, r.exec.count("stop hello.service"), r.heldFences())
	}
	r.exec.assertNothingStarted(t)
}

func TestReviewU4LatestTXIDAndWrongDatabaseRefuseUnderFence(t *testing.T) {
	r := reviewU4Rig(t)
	_, point := r.prepare(context.Background(), "")
	if point == nil {
		t.Fatal("prepare failed")
	}
	args := []dispatch.RestoreTestArgs{
		{App: "hello", Database: "main"},
		{App: "hello", Database: "main", TXID: "7"},
		{App: "hello", Database: "audit", Point: point.PreparationID},
	}
	puts, gets := r.remote.counts()
	stops := r.exec.count(" stop ")
	for _, input := range args {
		if state := reviewU4RunRestore(r, input); state != ops.Failed {
			t.Fatalf("ordinary or crossed source bypassed refusal: %+v state=%s", input, state)
		}
	}
	afterPuts, afterGets := r.remote.counts()
	if puts != afterPuts || gets != afterGets || stops != r.exec.count(" stop ") || r.heldFences() != 1 {
		t.Fatal("refused point test produced effects")
	}
	r.exec.assertNothingStarted(t)
}

func TestSQLiteSourceRejectsReplacedRootAndAncestor(t *testing.T) {
	for _, changed := range []string{"root", "ancestor", "symlink"} {
		t.Run(changed, func(t *testing.T) { testSQLiteSourceRootName(t, changed) })
	}
}

func testSQLiteSourceRootName(t *testing.T, changed string) {
	t.Helper()
	parent := filepath.Join(t.TempDir(), "parent")
	root := filepath.Join(parent, "data")
	incarnation := data.AppIncarnationID(strings.Repeat("a", 32))
	database := data.DatabaseID(strings.Repeat("b", 32))
	relative, err := data.RelativeDirectory(incarnation, database)
	if err != nil {
		t.Fatal(err)
	}
	binding := data.SQLiteDatabaseBinding{IncarnationID: incarnation, DatabaseID: database, Name: "main", Root: data.PersistentRoot(root), RelativeDirectory: relative, Filename: "app.db"}
	directory := filepath.Join(root, relative)
	if err = os.MkdirAll(directory, 0700); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(directory, "app.db"), []byte("held physical bytes"), 0600); err != nil {
		t.Fatal(err)
	}
	source, err := openSQLiteSource(binding, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = source.Close() })
	if _, err = source.Recheck(t.Context()); err != nil {
		t.Fatal("unchanged named source refused", err)
	}
	name := root
	if changed != "root" {
		name = parent
	}
	moved := name + ".held"
	if err = os.Rename(name, moved); err != nil {
		t.Fatal(err)
	}
	if changed == "symlink" {
		err = os.Symlink(moved, name)
	} else {
		err = os.MkdirAll(directory, 0700)
	}
	if err != nil {
		t.Fatal(err)
	}
	if _, err = source.Recheck(t.Context()); err == nil {
		t.Fatal("detached root accepted after", changed)
	}
}

func TestSQLitePreparationWriterOwnershipBeforeClaim(t *testing.T) {
	for _, changed := range []string{"artifact", "fragment", "dropin", "reload", "process"} {
		t.Run(changed, func(t *testing.T) { testSQLiteWriterOwnership(t, changed) })
	}
}

func testSQLiteWriterOwnership(t *testing.T, changed string) {
	t.Helper()
	r := newSQLitePrepRig(t, []string{"v2"}, "v2")
	o := r.operations()
	properties := r.exec.loadedWriter("hello.service", "SourcePath").Stdout
	switch changed {
	case "artifact":
		if err := os.WriteFile(filepath.Join(r.home, quadlet.ActiveDirectory, "hello.container"), []byte("foreign unit"), 0600); err != nil {
			t.Fatal(err)
		}
	case "fragment":
		properties = strings.Replace(properties, "FragmentPath=", "FragmentPath=/foreign/", 1)
	case "dropin":
		properties = strings.Replace(properties, "DropInPaths=", "DropInPaths=/foreign/override.conf", 1)
	case "reload":
		properties = strings.Replace(properties, "NeedDaemonReload=no", "NeedDaemonReload=yes", 1)
	case "process":
		r.writeProc(fmt.Sprintf("%d/cgroup", sqlitePrepWriterPID), "0::/user.slice/app.slice/foreign.service\n")
	}
	executor := reviewU4Executor(func(ctx context.Context, c localexec.Command) (localexec.Result, error) {
		if c.Path == "/usr/bin/systemctl" && len(c.Args) == 5 && c.Args[2] == "hello.service" {
			return localexec.Result{Stdout: properties}, nil
		}
		return r.exec.Execute(ctx, c)
	})
	session, err := localexec.NewSession(executor, nativeBackupFixtureUID(t), "/", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	o.host.session = session
	op, receipt := reviewU4PrepareUsing(r, o)
	if receipt != nil || op.State != ops.Failed || r.heldFences() != 0 || r.exec.count(" stop ") != 0 {
		t.Fatalf("unproved writer admitted for %s: %s", changed, op.State)
	}
	puts, gets := r.remote.counts()
	if puts != 0 || gets != 0 {
		t.Fatal("writer ownership refusal issued remote effects")
	}
	r.exec.assertNothingStarted(t)
}

func reviewU4PrepareUsing(r *sqlitePrepRig, o sqlitePreparations) (ops.Operation, *sqliteprep.Receipt) {
	r.t.Helper()
	a, err := o.Prepare(r.t.Context(), dispatch.SQLitePrepareArgs{App: "hello", Database: "main", IdempotencyKey: r.key()})
	if err != nil {
		r.t.Fatal(err)
	}
	_ = r.runner(o).Run(r.t.Context(), a.OperationID)
	return r.outcome(a.OperationID)
}

func TestSQLitePreparationRechecksWriterAfterClaim(t *testing.T) {
	r := newSQLitePrepRig(t, []string{"v2"}, "v2")
	o := r.operations()
	changed := false
	executor := reviewU4Executor(func(ctx context.Context, c localexec.Command) (localexec.Result, error) {
		var writeErr error
		if c.Path == "/usr/bin/systemctl" && len(c.Args) == 4 && c.Args[2] == "hello.service" && r.heldFences() == 1 && !changed {
			changed = true
			path := filepath.Join(r.home, ".config/systemd/user/hello.service")
			writeErr = os.WriteFile(path, []byte("foreign writer"), 0600)
		}
		if writeErr != nil {
			return localexec.Result{}, writeErr
		}
		return r.exec.Execute(ctx, c)
	})
	session, err := localexec.NewSession(executor, nativeBackupFixtureUID(t), "/", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	o.host.session = session
	op, receipt := reviewU4PrepareUsing(r, o)
	if !changed || receipt != nil || op.State != ops.RecoveryRequired || r.heldFences() != 1 || r.exec.count(" stop ") != 0 {
		t.Fatalf("writer changed after claim was stopped: changed=%t state=%s", changed, op.State)
	}
	r.exec.assertNothingStarted(t)
}
