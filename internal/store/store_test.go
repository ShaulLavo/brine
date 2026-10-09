package store

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"

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
func openTest(t testing.TB) *Store {
	t.Helper()
	s, e := Open(stateDir(t))
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { s.Close() })
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
	op, _, e := s.CreateOperation(context.Background(), id, "requester", "key")
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
	same, existing, e := s.CreateOperation(context.Background(), id, "requester", "key")
	if e != nil || !existing || same.ID != op {
		t.Fatalf("retry %s %v %v", same, existing, e)
	}
	other, existing, e := s.CreateOperation(context.Background(), id, "other", "key")
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
	op, _, e := s.CreateOperation(ctx, id, "requester", "key")
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
	if _, _, e = s.CreateOperation(ctx, second, "requester", "key"); !errors.Is(e, ErrConflict) {
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
			op, _, e := s.CreateOperation(ctx, id, "requester", "retry")
			if e != nil {
				t.Error(e)
				return
			}
			ids <- op.ID
			if _, e = s.AppendEvent(ctx, op.ID, Event{Kind: "launch", Payload: []byte(`{"outcome":"intent"}`)}); e != nil {
				t.Error(e)
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
	if _, _, e := s.CreateOperation(ctx, "missing", "requester", "other"); !errors.Is(e, ErrNotFound) {
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
		op, _, e := a.CreateOperation(ctx, id, "requester", fmt.Sprintf("race-%d", iteration))
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
