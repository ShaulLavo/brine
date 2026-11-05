//go:build linux

package store

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ShaulLavo/brine/internal/ops"
	"github.com/ShaulLavo/brine/internal/plan"
	"github.com/ShaulLavo/brine/internal/policy"
	"github.com/ShaulLavo/brine/internal/spec"
	"github.com/ShaulLavo/brine/internal/target"
)

func fixture(t testing.TB) plan.Input {
	t.Helper()
	read := func(p string) []byte {
		b, e := os.ReadFile(p)
		if e != nil {
			t.Fatal(e)
		}
		return b
	}
	s, e := target.Decode(read("../target/testdata/ready-arm64.json"))
	if e != nil {
		t.Fatal(e)
	}
	pol, e := policy.Parse(bytes.ReplaceAll(read("../policy/testdata/operator.toml"), []byte("Registry.Example.com:5000"), []byte("ghcr.io")))
	if e != nil {
		t.Fatal(e)
	}
	a, e := spec.Parse(bytes.ReplaceAll(read("../spec/testdata/valid-minimal.toml"), []byte("example/hello"), []byte("team/hello")))
	if e != nil {
		t.Fatal(e)
	}
	d, e := policy.Normalize(a, pol)
	if e != nil {
		t.Fatal(e)
	}
	d.Environment = []policy.Environment{{Name: "APP_ENV", Value: "production"}}
	return plan.Input{Desired: d, Snapshot: s, Image: plan.Image{Digest: strings.Split(string(d.Image), "@")[1], Platform: target.Platform{OS: "linux", Arch: s.Arch}, ManifestDigest: target.Known("sha256:" + strings.Repeat("b", 64))}, State: plan.BrineState{Target: s.Identity, Generation: *s.Generation.Value, Releases: []plan.CurrentRelease{}}}
}

var pristineTestControlDB = sync.OnceValues(newPristineTestControlDB)

func newPristineTestControlDB() (data []byte, err error) {
	dir, err := os.MkdirTemp("", "brine-store-schema-")
	if err != nil {
		return nil, err
	}
	defer func() { err = errors.Join(err, os.RemoveAll(dir)) }()
	s, err := Open(dir)
	if err != nil {
		return nil, err
	}
	if err := s.Close(); err != nil {
		return nil, err
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, err
	}
	defer func() { err = errors.Join(err, root.Close()) }()
	return root.ReadFile("control.db")
}

func openTest(t testing.TB) *Store {
	t.Helper()
	data, e := pristineTestControlDB()
	if e != nil {
		t.Fatal(e)
	}
	dir := stateDir(t)
	if e := os.WriteFile(filepath.Join(dir, "control.db"), data, 0600); e != nil {
		t.Fatal(e)
	}
	s, e := Open(dir)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() {
		if err := s.Close(); err != nil {
			t.Error(err)
		}
	})
	return s
}
func saved(t testing.TB, s *Store) (PlanID, plan.Input) {
	t.Helper()
	in := fixture(t)
	p, e := plan.Build(in)
	if e != nil {
		t.Fatal(e)
	}
	id, e := s.SavePlan(context.Background(), p, in.Desired)
	if e != nil {
		t.Fatal(e)
	}
	return id, in
}
func operation(t testing.TB, s *Store) OpID {
	t.Helper()
	id, _ := saved(t, s)
	op, _, e := s.CreateOperation(context.Background(), ops.Intent{Kind: ops.Deploy, PlanID: id}, "requester", "key")
	if e != nil {
		t.Fatal(e)
	}
	return op.ID
}
func TestMigrationsAndPragmas(t *testing.T) {
	dir := stateDir(t)
	s, e := Open(dir)
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	for k, want := range map[string]string{"journal_mode": "wal", "synchronous": "2", "foreign_keys": "1", "busy_timeout": "5000"} {
		var got string
		if e = s.db.QueryRow("PRAGMA " + k).Scan(&got); e != nil || got != want {
			t.Fatalf("%s = %s, %v", k, got, e)
		}
	}
	info, e := os.Stat(filepath.Join(dir, "control.db"))
	if e != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("mode %v %v", info, e)
	}
	if _, e = s.db.Exec("UPDATE schema_version SET version=999"); e != nil {
		t.Fatal(e)
	}
	s.Close()
	_, e = Open(dir)
	var schema *SchemaError
	if !errors.As(e, &schema) {
		t.Fatalf("downgrade: %v", e)
	}
}
func TestPlansVerifiedAndTamperRefused(t *testing.T) {
	s := openTest(t)
	id, in := saved(t, s)
	got, desired, e := s.LoadPlan(context.Background(), id)
	if e != nil {
		t.Fatal(e)
	}
	if !reflect.DeepEqual(desired, in.Desired) {
		t.Fatal("literal normalized input lost")
	}
	bad := in
	bad.Desired.Environment = []policy.Environment{{Name: "APP_ENV", Value: "changed"}}
	if _, e = s.SavePlan(context.Background(), got, bad.Desired); e == nil {
		t.Fatal("mismatched input saved")
	}
	if _, e = s.db.Exec("UPDATE plans SET desired_hash='sha256:bad' WHERE id=?", id); e != nil {
		t.Fatal(e)
	}
	_, _, e = s.LoadPlan(context.Background(), id)
	var integrity *IntegrityError
	if !errors.As(e, &integrity) {
		t.Fatalf("tamper: %v", e)
	}
}
func TestOperationsIdempotencyAndTransitions(t *testing.T) {
	s := openTest(t)
	op := operation(t, s)
	id, _ := saved(t, s)
	same, existing, e := s.CreateOperation(context.Background(), ops.Intent{Kind: ops.Deploy, PlanID: id}, "requester", "key")
	if e != nil || !existing || same.ID != op {
		t.Fatalf("retry %s %v %v", same, existing, e)
	}
	other, existing, e := s.CreateOperation(context.Background(), ops.Intent{Kind: ops.Deploy, PlanID: id}, "other", "key")
	if e != nil || existing || other.ID == op {
		t.Fatal("requester namespace")
	}
	if e = s.SetOperationState(context.Background(), op, ops.Checking); e == nil {
		t.Fatal("illegal transition")
	}
	for _, state := range []State{ops.Preflight, ops.Preparing, ops.Quiescing, ops.Starting, ops.Checking, ops.Committing, ops.Succeeded} {
		if e = s.SetOperationState(context.Background(), op, state); e != nil {
			t.Fatal(e)
		}
	}
	if e = s.SetOperationState(context.Background(), op, ops.Failed); e == nil {
		t.Fatal("terminal transition")
	}
	unfinished, e := s.ListUnfinished(context.Background())
	if e != nil || len(unfinished) != 1 || unfinished[0].ID != other.ID {
		t.Fatalf("unfinished %+v %v", unfinished, e)
	}
}
func TestConcurrentEventOrdering(t *testing.T) {
	s := openTest(t)
	op := operation(t, s)
	const n = 128
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if _, e := s.AppendEvent(context.Background(), op, Event{Kind: "step", Payload: []byte(`{"step":"stage_unit","outcome":"intent"}`)}); e != nil {
				t.Error(e)
			}
		}(i)
	}
	wg.Wait()
	events, e := s.EventsAfter(context.Background(), op, 0, n)
	if e != nil || len(events) != n {
		t.Fatalf("events %d %v", len(events), e)
	}
	for i, event := range events {
		if event.Sequence != uint64(i+1) {
			t.Fatal("nonmonotonic sequence")
		}
	}
	if _, e = s.db.Exec("DELETE FROM events WHERE operation_id=?", op); e == nil {
		t.Fatal("event deletion allowed")
	}
	if _, e = s.db.Exec("UPDATE events SET kind='changed' WHERE operation_id=?", op); e == nil {
		t.Fatal("event update allowed")
	}
	if _, e = s.AppendEvent(context.Background(), op, Event{Kind: "step", Payload: bytes.Repeat([]byte("x"), MaxEventBytes+1)}); e == nil {
		t.Fatal("unbounded payload")
	}
}

func stateDir(t testing.TB) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	return dir
}

func release(t testing.TB, s *Store, id string) Release {
	t.Helper()
	planID, _ := saved(t, s)
	p, _, e := s.LoadPlan(context.Background(), planID)
	if e != nil {
		t.Fatal(e)
	}
	return Release{ID: id, PlanID: planID, Image: p.Image, HostPort: p.HostPort, Secrets: p.Secrets, Units: []target.Unit{{Name: "hello.container", Hash: "sha256:" + strings.Repeat("b", 64)}, {Name: "hello.volume", Hash: "sha256:" + strings.Repeat("c", 64)}}, CaddyFile: target.CaddyFile{Name: "hello.caddy", Hash: "sha256:" + strings.Repeat("d", 64)}, CaddyGeneration: 2}
}
func TestReleaseHeadsAndBrineStateRoundTrip(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	r := release(t, s, "release-0001")
	if e := s.CommitRelease(ctx, "hello", r); e != nil {
		t.Fatal(e)
	}
	in := fixture(t)
	raw, e := os.ReadFile("../target/testdata/one-app.json")
	if e != nil {
		t.Fatal(e)
	}
	in.Snapshot, e = target.Decode(raw)
	if e != nil {
		t.Fatal(e)
	}
	in.State, e = s.LoadBrineState(ctx, in.Snapshot.Identity, *in.Snapshot.Generation.Value)
	if e != nil {
		t.Fatal(e)
	}
	p, e := plan.Build(in)
	if e != nil || p.Kind != plan.NoOp {
		t.Fatalf("round trip %s %v %+v", p.Kind, e, p.Conflicts)
	}
	r2 := r
	r2.ID = "release-0002"
	if e = s.CommitRelease(ctx, "hello", r2); e != nil {
		t.Fatal(e)
	}
	if e = s.CommitRelease(ctx, "hello", r2); e != nil {
		t.Fatal(e)
	}
	current, e := s.CurrentRelease(ctx, "hello")
	if e != nil || current.ID != r2.ID {
		t.Fatalf("current %+v %v", current, e)
	}
	previous, e := s.PreviousRelease(ctx, "hello")
	if e != nil || previous.ID != r.ID {
		t.Fatalf("previous %+v %v", previous, e)
	}
	if e = s.CommitRelease(ctx, "hello", r); e != nil {
		t.Fatal(e)
	}
	current, _ = s.CurrentRelease(ctx, "hello")
	if current.ID != r2.ID {
		t.Fatal("retry old commit moved head backwards")
	}
	r2.CaddyFile.Hash = "sha256:" + strings.Repeat("e", 64)
	if e = s.CommitRelease(ctx, "hello", r2); !errors.Is(e, ErrConflict) {
		t.Fatalf("release replacement %v", e)
	}
}
func TestPlanCanonicalAndDesiredTampering(t *testing.T) {
	for _, column := range []string{"canonical", "desired", "content_hash"} {
		t.Run(column, func(t *testing.T) {
			s := openTest(t)
			id, _ := saved(t, s)
			if _, e := s.db.Exec("UPDATE plans SET "+column+"=? WHERE id=?", []byte(`{}`), id); e != nil {
				t.Fatal(e)
			}
			_, _, e := s.LoadPlan(context.Background(), id)
			var integrity *IntegrityError
			if !errors.As(e, &integrity) {
				t.Fatalf("tamper %v", e)
			}
		})
	}
}
func TestIdempotencyConflictAcrossPlans(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	id, in := saved(t, s)
	op, _, e := s.CreateOperation(ctx, ops.Intent{Kind: ops.Deploy, PlanID: id}, "requester", "key")
	if e != nil {
		t.Fatal(e)
	}
	in.Desired.Environment[0].Value = "staging"
	p, e := plan.Build(in)
	if e != nil {
		t.Fatal(e)
	}
	second, e := s.SavePlan(ctx, p, in.Desired)
	if e != nil {
		t.Fatal(e)
	}
	if _, _, e = s.CreateOperation(ctx, ops.Intent{Kind: ops.Deploy, PlanID: second}, "requester", "key"); !errors.Is(e, ErrConflict) {
		t.Fatalf("key rebound %v", e)
	}
	if e = s.SetOperationState(ctx, op.ID, ops.Preflight); e != nil {
		t.Fatal(e)
	}
	if e = s.SetOperationState(ctx, op.ID, ops.Preflight); e != nil {
		t.Fatal(e)
	}
	events, e := s.EventsAfter(ctx, op.ID, 0, 100)
	if e != nil || len(events) != 1 || events[0].State != ops.Preflight {
		t.Fatalf("state journal %+v %v", events, e)
	}
}
func TestConcurrentStoresShareIdempotencyAndEventSequence(t *testing.T) {
	ctx := context.Background()
	dir := stateDir(t)
	a, e := Open(dir)
	if e != nil {
		t.Fatal(e)
	}
	defer a.Close()
	b, e := Open(dir)
	if e != nil {
		t.Fatal(e)
	}
	defer b.Close()
	id, _ := saved(t, a)
	const n = 64
	ids := make(chan string, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			s := a
			if i%2 != 0 {
				s = b
			}
			started := time.Now()
			op, _, e := s.CreateOperation(ctx, ops.Intent{Kind: ops.Deploy, PlanID: id}, "requester", "retry")
			if e != nil {
				t.Errorf("create request %d after %s: %v", i, time.Since(started), e)
				return
			}
			ids <- op.ID
			started = time.Now()
			if _, e = s.AppendEvent(ctx, op.ID, Event{Kind: "launch", Payload: []byte(`{"outcome":"intent"}`)}); e != nil {
				t.Errorf("append request %d after %s: %v", i, time.Since(started), e)
			}
		}(i)
	}
	wg.Wait()
	close(ids)
	var first string
	for id := range ids {
		if first == "" {
			first = id
		}
		if id != first {
			t.Fatal("duplicate operation")
		}
	}
	events, e := a.EventsAfter(ctx, first, 0, n)
	if e != nil || len(events) != n {
		t.Fatalf("events %d %v", len(events), e)
	}
	for i, event := range events {
		if event.Sequence != uint64(i+1) {
			t.Fatal("sequence gap")
		}
	}
}
func TestPrivateFilesAndSymlinks(t *testing.T) {
	dir := stateDir(t)
	other := filepath.Join(dir, "other")
	if e := os.WriteFile(other, []byte("untouched"), 0644); e != nil {
		t.Fatal(e)
	}
	if e := os.Symlink(other, filepath.Join(dir, "control.db")); e != nil {
		t.Fatal(e)
	}
	if _, e := Open(dir); e == nil {
		t.Fatal("followed database symlink")
	}
	b, e := os.ReadFile(other)
	if e != nil || string(b) != "untouched" {
		t.Fatal("changed symlink target")
	}
}

func TestSQLBoundaryAndMigrationRollback(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	op := operation(t, s)
	if _, e := s.db.Exec("UPDATE operations SET state='checking' WHERE id=?", op); e == nil {
		t.Fatal("SQL bypassed legal transition")
	}
	if _, e := s.db.Exec("INSERT INTO events VALUES(?,2,'launch','',?,?)", op, []byte(`{"outcome":"intent"}`), timestamp()); e == nil {
		t.Fatal("SQL skipped sequence")
	}
	if _, e := s.AppendEvent(ctx, "missing", Event{Kind: "launch", Payload: []byte(`{"outcome":"intent"}`)}); !errors.Is(e, ErrNotFound) {
		t.Fatalf("missing op %v", e)
	}
	if _, _, e := s.CreateOperation(ctx, ops.Intent{Kind: ops.Deploy, PlanID: "sha256:" + strings.Repeat("f", 64)}, "requester", "other"); !errors.Is(e, ErrNotFound) {
		t.Fatalf("missing plan %v", e)
	}
	dir := stateDir(t)
	db, e := sql.Open("sqlite", filepath.Join(dir, "control.db"))
	if e != nil {
		t.Fatal(e)
	}
	if _, e = db.Exec("CREATE TABLE plans (id TEXT)"); e != nil {
		t.Fatal(e)
	}
	db.Close()
	if store, e := Open(dir); e == nil {
		store.Close()
		t.Fatal("accepted interrupted incompatible schema")
	}
	db, e = sql.Open("sqlite", filepath.Join(dir, "control.db"))
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	var count int
	if e = db.QueryRow("SELECT count(*) FROM sqlite_master WHERE name IN ('schema_version','operations','events')").Scan(&count); e != nil || count != 0 {
		t.Fatalf("migration not transactional %d %v", count, e)
	}
}
func TestSecretReferenceOnlyJournalAndDBPermissions(t *testing.T) {
	s := openTest(t)
	op := operation(t, s)
	for _, event := range []Event{{Kind: "step", Payload: []byte(`{"step":"stage_unit","outcome":"intent","environment":"private"}`)}, {Kind: "failure", Payload: []byte(`{"code":"private"}`)}, {Kind: "launch", Payload: []byte(`{"outcome":"intent","secret":"private"}`)}} {
		if _, e := s.AppendEvent(context.Background(), op, event); e == nil {
			t.Fatal("value-bearing event accepted")
		}
	}
	for _, name := range []string{"control.db", "control.db-wal", "control.db-shm"} {
		info, e := os.Stat(filepath.Join(s.dir, name))
		if e != nil || info.Mode().Perm() != 0600 {
			t.Fatalf("private file %s %v %v", name, info, e)
		}
	}
}

var _ interface {
	AcquireHostLock(context.Context) (ops.Lock, error)
} = (*Store)(nil)

func TestConditionalTransitionsRaceWithoutOverwritingWinner(t *testing.T) {
	ctx := context.Background()
	dir := stateDir(t)
	a, e := Open(dir)
	if e != nil {
		t.Fatal(e)
	}
	defer a.Close()
	b, e := Open(dir)
	if e != nil {
		t.Fatal(e)
	}
	defer b.Close()
	id, _ := saved(t, a)
	for iteration := 0; iteration < 64; iteration++ {
		op, _, e := a.CreateOperation(ctx, ops.Intent{Kind: ops.Deploy, PlanID: id}, "requester", fmt.Sprintf("race-%d", iteration))
		if e != nil {
			t.Fatal(e)
		}
		type result struct {
			state ops.State
			err   error
		}
		results := make(chan result, 2)
		start := make(chan struct{})
		for i, state := range []ops.State{ops.Failed, ops.Preflight} {
			s := a
			if i == 1 {
				s = b
			}
			go func(s *Store, state ops.State) {
				<-start
				results <- result{state, s.TransitionOperation(ctx, op.ID, ops.Queued, state)}
			}(s, state)
		}
		close(start)
		first, second := <-results, <-results
		winner, loser := first, second
		if winner.err != nil {
			winner, loser = loser, winner
		}
		if winner.err != nil {
			t.Fatalf("no winner: %v / %v", first.err, second.err)
		}
		var conflict *ops.StateConflictError
		if !errors.Is(loser.err, ops.ErrStateConflict) || !errors.As(loser.err, &conflict) || conflict.Current != winner.state {
			t.Fatalf("loser conflict: %#v %v", conflict, loser.err)
		}
		got, e := a.GetOperation(ctx, op.ID)
		if e != nil || got.State != winner.state {
			t.Fatalf("winner overwritten: %s %v", got.State, e)
		}
		events, e := a.EventsAfter(ctx, op.ID, 0, 10)
		if e != nil || len(events) != 1 || events[0].State != winner.state {
			t.Fatalf("loser changed journal: %+v %v", events, e)
		}
	}
}
func TestConditionalTransitionRefusesIllegalEdge(t *testing.T) {
	s := openTest(t)
	op := operation(t, s)
	e := s.TransitionOperation(context.Background(), op, ops.Queued, ops.Checking)
	var conflict *ops.StateConflictError
	if !errors.Is(e, ops.ErrStateConflict) || !errors.As(e, &conflict) || conflict.Current != ops.Queued {
		t.Fatalf("illegal edge: %v", e)
	}
	events, e := s.EventsAfter(context.Background(), op, 0, 10)
	if e != nil || len(events) != 0 {
		t.Fatal("illegal transition changed journal")
	}
}

func TestPublicAppendCannotForgeState(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	op := operation(t, s)
	if _, e := s.AppendEvent(ctx, op, Event{Kind: "state", State: ops.Succeeded}); e == nil {
		t.Fatal("forged state event accepted")
	}
	got, e := s.GetOperation(ctx, op)
	if e != nil || got.State != ops.Queued {
		t.Fatal("forgery changed state")
	}
	events, e := s.EventsAfter(ctx, op, 0, 100)
	if e != nil || len(events) != 0 {
		t.Fatal("forgery changed journal")
	}
	for _, state := range []ops.State{ops.Preflight, ops.Preparing, ops.Quiescing, ops.Starting, ops.Checking, ops.Committing, ops.Succeeded} {
		if e = s.SetOperationState(ctx, op, state); e != nil {
			t.Fatal(e)
		}
		if e = s.SetOperationState(ctx, op, state); e != nil {
			t.Fatal(e)
		}
	}
	events, e = s.EventsAfter(ctx, op, 0, 100)
	if e != nil || len(events) != 7 {
		t.Fatalf("state events %d %v", len(events), e)
	}
	for i, state := range []ops.State{ops.Preflight, ops.Preparing, ops.Quiescing, ops.Starting, ops.Checking, ops.Committing, ops.Succeeded} {
		if events[i].Kind != "state" || events[i].State != state {
			t.Fatal("state event mismatch")
		}
	}
}
func TestOpenSecuresExistingFilesAndDirectory(t *testing.T) {
	dir := stateDir(t)
	for _, name := range []string{"control.db", "control.db-wal", "control.db-shm"} {
		if e := os.WriteFile(filepath.Join(dir, name), nil, 0644); e != nil {
			t.Fatal(e)
		}
	}
	s, e := Open(dir)
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	for _, name := range []string{"control.db", "control.db-wal", "control.db-shm"} {
		info, e := os.Stat(filepath.Join(dir, name))
		if e != nil || info.Mode().Perm() != 0600 {
			t.Fatalf("insecure restored %s %v %v", name, info, e)
		}
		if e = os.Chmod(filepath.Join(dir, name), 0644); e != nil {
			t.Fatal(e)
		}
	}
	if e = os.Chmod(dir, 0755); e != nil {
		t.Fatal(e)
	}
	second, e := Open(dir)
	if e != nil {
		t.Fatal(e)
	}
	defer second.Close()
	info, e := os.Stat(dir)
	if e != nil || info.Mode().Perm() != 0700 {
		t.Fatalf("insecure directory %v %v", info, e)
	}
	for _, name := range []string{"control.db", "control.db-wal", "control.db-shm"} {
		info, e := os.Stat(filepath.Join(dir, name))
		if e != nil || info.Mode().Perm() != 0600 {
			t.Fatalf("insecure reopened %s %v %v", name, info, e)
		}
	}
}
func TestOpenRefusesAuxiliaryFileSymlinks(t *testing.T) {
	for _, name := range []string{"control.db-wal", "control.db-shm"} {
		t.Run(name, func(t *testing.T) {
			dir := stateDir(t)
			victim := filepath.Join(dir, "victim")
			if e := os.WriteFile(victim, []byte("untouched"), 0644); e != nil {
				t.Fatal(e)
			}
			if e := os.Symlink(victim, filepath.Join(dir, name)); e != nil {
				t.Fatal(e)
			}
			if s, e := Open(dir); e == nil {
				s.Close()
				t.Fatal("followed sidecar symlink")
			}
			info, e := os.Stat(victim)
			if e != nil || info.Mode().Perm() != 0644 {
				t.Fatal("changed symlink target")
			}
		})
	}
}

func TestOpenRepairsExistingSQLiteSidecars(t *testing.T) {
	dir := stateDir(t)
	first, e := Open(dir)
	if e != nil {
		t.Fatal(e)
	}
	defer first.Close()
	for _, name := range []string{"control.db-wal", "control.db-shm"} {
		if e = os.Chmod(filepath.Join(dir, name), 0644); e != nil {
			t.Fatal(e)
		}
	}
	second, e := Open(dir)
	if e != nil {
		t.Fatal(e)
	}
	defer second.Close()
	for _, name := range []string{"control.db-wal", "control.db-shm"} {
		info, e := os.Stat(filepath.Join(dir, name))
		if e != nil || info.Mode().Perm() != 0600 {
			t.Fatalf("insecure existing sidecar %s %v %v", name, info, e)
		}
	}
}

func TestAppHistoryQueries(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	r := release(t, s, "release-one")
	if e := s.CommitRelease(ctx, "hello", r); e != nil {
		t.Fatal(e)
	}
	got, e := s.ReleaseByID(ctx, "hello", r.ID)
	if e != nil || !reflect.DeepEqual(got, r) {
		t.Fatal(got, e)
	}
	if _, e = s.ReleaseByID(ctx, "other", r.ID); !errors.Is(e, ErrNotFound) {
		t.Fatal(e)
	}
	if _, e = s.LastOperation(ctx, "hello"); !errors.Is(e, ErrNotFound) {
		t.Fatal(e)
	}
	first, _, e := s.CreateOperation(ctx, ops.Intent{Kind: ops.Deploy, PlanID: r.PlanID}, "requester", "first")
	if e != nil {
		t.Fatal(e)
	}
	if e = s.SetOperationState(ctx, first.ID, ops.Failed); e != nil {
		t.Fatal(e)
	}
	second, _, e := s.CreateOperation(ctx, ops.Intent{Kind: ops.Deploy, PlanID: r.PlanID}, "requester", "second")
	if e != nil {
		t.Fatal(e)
	}
	if e = s.SetOperationState(ctx, second.ID, ops.Failed); e != nil {
		t.Fatal(e)
	}
	gotOp, e := s.LastOperation(ctx, "hello")
	if e != nil || gotOp.ID != second.ID || gotOp.State != ops.Failed || gotOp.PlanID != r.PlanID {
		t.Fatal(gotOp, e)
	}
	if _, e = s.LastOperation(ctx, "other"); !errors.Is(e, ErrNotFound) {
		t.Fatal(e)
	}
	if _, e = s.db.Exec(`UPDATE plans SET desired=? WHERE id=?`, []byte(`{"name":"hello"}`), r.PlanID); e != nil {
		t.Fatal(e)
	}
	if _, e = s.LastOperation(ctx, "hello"); e == nil {
		t.Fatal("tampered plan accepted")
	}
}

func TestLoadBrineStateCurrentPlanID(t *testing.T) {
	s := openTest(t)
	r := release(t, s, "current")
	if err := s.CommitRelease(t.Context(), "hello", r); err != nil {
		t.Fatal(err)
	}
	in := fixture(t)
	in.Desired.Name = "zeta"
	in.Desired.Domains = []spec.Domain{"zeta.example.com"}
	p, err := plan.Build(in)
	if err != nil {
		t.Fatal(err)
	}
	id, err := s.SavePlan(t.Context(), p, in.Desired)
	if err != nil {
		t.Fatal(err)
	}
	other := Release{ID: "zeta-current", PlanID: id, Image: p.Image, HostPort: p.HostPort, Secrets: p.Secrets, Units: []target.Unit{{Name: "zeta.container", Hash: r.Units[0].Hash}}, CaddyFile: target.CaddyFile{Name: "zeta.caddy", Hash: r.CaddyFile.Hash}}
	if err := s.CommitRelease(t.Context(), "zeta", other); err != nil {
		t.Fatal(err)
	}
	state, err := s.LoadBrineState(t.Context(), in.Snapshot.Identity, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(state.Releases) != 2 || state.Releases[0].App != "hello" || state.Releases[0].PlanID != r.PlanID || state.Releases[1].App != "zeta" || state.Releases[1].PlanID != id || id == r.PlanID {
		t.Fatalf("validated current plan IDs missing or crossed apps: %+v", state.Releases)
	}
}

func TestLoadBrineStateRejectsCurrentIntegrityFailures(t *testing.T) {
	for _, kind := range []string{"release_body", "release_hash", "release_id", "release_plan", "release_image", "plan_body", "plan_hash", "plan_app", "plan_policy", "plan_schema", "desired_body", "desired_hash", "target"} {
		t.Run(kind, func(t *testing.T) { checkCurrentIntegrityFailure(t, kind) })
	}
}

func checkCurrentIntegrityFailure(t *testing.T, kind string) {
	t.Helper()
	s := openTest(t)
	r := release(t, s, "current")
	if err := s.CommitRelease(t.Context(), "hello", r); err != nil {
		t.Fatal(err)
	}
	identity := fixture(t).Snapshot.Identity
	if kind == "target" {
		identity.ID = "different-target"
	} else {
		corruptCurrentRecord(t, s, r, kind)
	}
	state, err := s.LoadBrineState(t.Context(), identity, 1)
	var integrity *IntegrityError
	if !errors.As(err, &integrity) || len(state.Releases) != 0 {
		t.Fatalf("corrupt %s returned state %+v, error %v", kind, state.Releases, err)
	}
}

func corruptCurrentRecord(t *testing.T, s *Store, r Release, kind string) {
	t.Helper()
	if strings.HasPrefix(kind, "release_") {
		corruptReleaseRecord(t, s, r, kind)
		return
	}
	p, _, err := s.LoadPlan(t.Context(), r.PlanID)
	if err != nil {
		t.Fatal(err)
	}
	var raw []byte
	switch kind {
	case "plan_body", "desired_body":
		raw = []byte(`{}`)
	case "plan_hash", "desired_hash":
		raw = []byte("sha256:" + strings.Repeat("f", 64))
	case "plan_app", "plan_policy", "plan_schema":
		if kind == "plan_app" {
			p.App = "other"
		}
		if kind == "plan_policy" {
			p.PolicyVersion = "other-policy"
		}
		if kind == "plan_schema" {
			p.SchemaVersion++
		}
		raw, err = p.CanonicalBytes()
	}
	if err != nil {
		t.Fatal(err)
	}
	queries := map[string]string{
		"plan_body":    "UPDATE plans SET canonical=? WHERE id=?",
		"plan_hash":    "UPDATE plans SET content_hash=? WHERE id=?",
		"desired_body": "UPDATE plans SET desired=? WHERE id=?",
		"desired_hash": "UPDATE plans SET desired_hash=? WHERE id=?",
	}
	if query, ok := queries[kind]; ok {
		_, err = s.db.ExecContext(t.Context(), query, raw, r.PlanID)
	} else {
		_, err = s.db.ExecContext(t.Context(), "UPDATE plans SET canonical=?,content_hash=? WHERE id=?", raw, digest(raw), r.PlanID)
	}
	if err != nil {
		t.Fatal(err)
	}
}

func corruptReleaseRecord(t *testing.T, s *Store, r Release, kind string) {
	t.Helper()
	if _, err := s.db.ExecContext(t.Context(), "DROP TRIGGER releases_no_update"); err != nil {
		t.Fatal(err)
	}
	if kind == "release_hash" {
		if _, err := s.db.ExecContext(t.Context(), "UPDATE releases SET content_hash='invalid' WHERE id=?", r.ID); err != nil {
			t.Fatal(err)
		}
		return
	}
	id := r.ID
	switch kind {
	case "release_id":
		r.ID = "another-release"
	case "release_plan":
		r.PlanID = "another-plan"
	case "release_image":
		r.Image.Digest = "sha256:" + strings.Repeat("e", 64)
	}
	raw, err := json.Marshal(r)
	if kind == "release_body" {
		raw = []byte(`{}`)
	}
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.ExecContext(t.Context(), "UPDATE releases SET content=?,content_hash=? WHERE id=?", raw, digest(raw), id); err != nil {
		t.Fatal(err)
	}
}
