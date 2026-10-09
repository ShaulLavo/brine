package secrets

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ShaulLavo/brine/internal/localexec"
	"github.com/ShaulLavo/brine/internal/ops"
	"github.com/ShaulLavo/brine/internal/podman"
	"github.com/ShaulLavo/brine/internal/policy"
	"github.com/ShaulLavo/brine/internal/store"
)

type fakePodman struct {
	dir     string
	mu      sync.Mutex
	names   []string
	calls   []localexec.Command
	unknown bool
	created bool
}

func (f *fakePodman) Execute(_ context.Context, c localexec.Command) (localexec.Result, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	copied := c
	copied.Stdin = append([]byte(nil), c.Stdin...)
	f.calls = append(f.calls, copied)
	switch strings.Join(c.Args[:2], " ") {
	case "secret ls":
		return localexec.Result{Stdout: strings.Join(f.names, "\n")}, nil
	case "secret create":
		if !f.unknown || f.created {
			f.names = append(f.names, c.Args[2])
		}
		if f.unknown {
			return localexec.Result{Stderr: string(c.Stdin)}, &localexec.Error{Kind: localexec.UnknownOutcome}
		}
		return localexec.Result{Stdout: "secret-id"}, nil
	case "secret exists":
		for _, n := range f.names {
			if n == c.Args[2] {
				return localexec.Result{}, nil
			}
		}
		return localexec.Result{}, &localexec.Error{Kind: localexec.Failed, ExitCode: 1}
	}
	return localexec.Result{}, errors.New("unexpected command")
}
func secretFixture(t *testing.T) (Service, *store.Store, *fakePodman) {
	t.Helper()
	dir := t.TempDir()
	os.Chmod(dir, 0700)
	db, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	raw, err := os.ReadFile("../policy/testdata/operator.toml")
	if err != nil {
		t.Fatal(err)
	}
	p, err := policy.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	fake := &fakePodman{dir: dir}
	session, err := localexec.NewSession(fake, 1000, dir, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	return Service{Store: db, Podman: podman.New(session), LoadPolicy: func(context.Context) (policy.Policy, error) { return p, nil }, Requester: "agent"}, db, fake
}
func TestSecretSetBeforeDeployAndNoValueInAudit(t *testing.T) {
	s, db, f := secretFixture(t)
	value := []byte("PLANTED_PRIVATE_SECRET")
	got, err := s.Set(context.Background(), "hello", "hello-token", "request1", value)
	if err != nil {
		t.Fatal(err)
	}
	if got.VersionName != "brine.hello.hello-token.v1" {
		t.Fatal(got)
	}
	op, err := db.GetOperation(context.Background(), got.OperationID)
	if err != nil || op.Kind != ops.SecretSet || op.PlanID != "" || op.State != ops.Succeeded {
		t.Fatal(op, err)
	}
	events, err := db.EventsAfter(context.Background(), op.ID, 0, 20)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(events)
	if strings.Contains(string(b), string(value)) {
		t.Fatal("value in audit")
	}
	b, _ = json.Marshal(got)
	if strings.Contains(string(b), string(value)) {
		t.Fatal("value in result")
	}
	create := 0
	for _, c := range f.calls {
		if strings.Contains(strings.Join(c.Args, " "), string(value)) || strings.Contains(strings.Join(c.Env, " "), string(value)) {
			t.Fatal("value in argv or env")
		}
		if c.Args[1] == "create" {
			create++
			if string(c.Stdin) != string(value) || !c.Mutation || c.Timeout <= 0 || c.Args[3] != "-" {
				t.Fatal("not bounded stdin mutation")
			}
		}
	}
	if create != 1 {
		t.Fatal("wrong create count", create)
	}
	again, err := s.Set(context.Background(), "hello", "hello-token", "request1", value)
	if err != nil || again != got {
		t.Fatal(again, err)
	}
	if len(f.names) != 1 {
		t.Fatal("retry created a new secret")
	}
}
func TestSecretPolicyRefusal(t *testing.T) {
	s, _, f := secretFixture(t)
	_, err := s.Set(context.Background(), "other", "hello-token", "req", []byte("PRIVATE"))
	if err == nil || len(f.calls) != 0 {
		t.Fatal("policy bypass", err)
	}
}
func TestSecretConcurrentVersions(t *testing.T) {
	s, _, f := secretFixture(t)
	second, err := store.Open(f.dir)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	s2 := s
	s2.Store = second
	const n = 24
	var wg sync.WaitGroup
	fail := make(chan error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			service := s
			if i%2 == 0 {
				service = s2
			}
			_, err := service.Set(context.Background(), "hello", "hello-token", fmt.Sprintf("request%d", i), []byte("PRIVATE"))
			fail <- err
		}(i)
	}
	wg.Wait()
	close(fail)
	for err := range fail {
		if err != nil {
			t.Fatal(err)
		}
	}
	names := map[string]bool{}
	for _, name := range f.names {
		names[name] = true
	}
	for i := 1; i <= n; i++ {
		if !names["brine.hello.hello-token.v"+strconv.Itoa(i)] {
			t.Fatal("missing version", i)
		}
	}
}
func TestUnknownSecretCreateInspectsRatherThanRetries(t *testing.T) {
	for _, created := range []bool{false, true} {
		t.Run(strconv.FormatBool(created), func(t *testing.T) {
			s, db, f := secretFixture(t)
			f.unknown = true
			f.created = created
			got, err := s.Set(context.Background(), "hello", "hello-token", "req", []byte("PLANTED_PRIVATE_SECRET"))
			if created && err != nil || !created && err == nil {
				t.Fatal(got, err)
			}
			op, err := db.LastOperation(context.Background(), "hello")
			if err != nil {
				t.Fatal(err)
			}
			want := ops.RecoveryRequired
			if created {
				want = ops.Succeeded
			}
			if op.State != want {
				t.Fatal(op.State)
			}
			events, err := db.EventsAfter(context.Background(), op.ID, 0, 20)
			if err != nil {
				t.Fatal(err)
			}
			b, _ := json.Marshal(events)
			if strings.Contains(string(b), "PLANTED_PRIVATE_SECRET") {
				t.Fatal("error leaked")
			}
			s.Set(context.Background(), "hello", "hello-token", "req", []byte("PRIVATE"))
			create := 0
			for _, c := range f.calls {
				if c.Args[1] == "create" {
					create++
				}
			}
			if create != 1 {
				t.Fatal("blind retry")
			}
		})
	}
}

func TestUnknownVersionReservationSurvivesAbsentRuntimeName(t *testing.T) {
	s, db, f := secretFixture(t)
	f.unknown = true
	if _, err := s.Set(context.Background(), "hello", "hello-token", "uncertain", []byte("PRIVATE")); err == nil {
		t.Fatal("expected unknown")
	}
	latest, err := db.LatestSecretVersion(context.Background(), "hello", "hello-token")
	if err != nil || latest != 1 {
		t.Fatal(latest, err)
	}
	f.unknown = false
	got, err := s.Set(context.Background(), "hello", "hello-token", "next", []byte("PRIVATE"))
	if err != nil || got.VersionName != "brine.hello.hello-token.v2" {
		t.Fatal(got, err)
	}
}

func TestPreparingSecretReconcilesWithoutCreating(t *testing.T) {
	for _, exists := range []bool{false, true} {
		t.Run(strconv.FormatBool(exists), func(t *testing.T) {
			s, db, f := secretFixture(t)
			ctx := context.Background()
			op, _, err := db.CreateOperation(ctx, ops.Intent{Kind: ops.SecretSet, App: "hello", SecretRef: "hello-token"}, "agent", "crashed")
			if err != nil {
				t.Fatal(err)
			}
			if err = db.SetOperationState(ctx, op.ID, ops.Preparing); err != nil {
				t.Fatal(err)
			}
			name := "brine.hello.hello-token.v7"
			payload, _ := json.Marshal(ops.SecretVersionPayload{Name: name, Outcome: "intent"})
			if _, err = db.AppendEvent(ctx, op.ID, ops.Event{Kind: "secret_version", Payload: payload}); err != nil {
				t.Fatal(err)
			}
			if exists {
				f.names = []string{name}
			}
			got, err := s.Set(ctx, "hello", "hello-token", "crashed", []byte("DIFFERENT_PRIVATE_VALUE"))
			if exists && (err != nil || got.VersionName != name) || !exists && err == nil {
				t.Fatal(got, err)
			}
			for _, c := range f.calls {
				if c.Args[1] == "create" {
					t.Fatal("replayed uncertain effect")
				}
			}
			after, err := db.GetOperation(ctx, op.ID)
			want := ops.RecoveryRequired
			if exists {
				want = ops.Succeeded
			}
			if err != nil || after.State != want {
				t.Fatal(after, err)
			}
		})
	}
}

func TestVersionExhaustionDoesNotCreate(t *testing.T) {
	s, _, f := secretFixture(t)
	f.names = []string{"brine.hello.hello-token.v18446744073709551615"}
	if _, err := s.Set(context.Background(), "hello", "hello-token", "overflow", []byte("PRIVATE")); err == nil {
		t.Fatal("version overflow accepted")
	}
	for _, c := range f.calls {
		if c.Args[1] == "create" {
			t.Fatal("created overflowing version")
		}
	}
}

type slowList struct{ Runtime }

func (s slowList) SecretNames(ctx context.Context) ([]podman.Name, error) {
	select {
	case <-time.After(JournalTimeout + 100*time.Millisecond):
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	return s.Runtime.SecretNames(ctx)
}
func TestSlowSuccessfulListHasFreshReservationJournal(t *testing.T) {
	s, db, _ := secretFixture(t)
	s.Podman = slowList{s.Podman}
	got, err := s.Set(context.Background(), "hello", "hello-token", "slow", []byte("PRIVATE"))
	if err != nil {
		t.Fatal("allocation outlived journal context", err)
	}
	op, err := db.GetOperation(context.Background(), got.OperationID)
	if err != nil || op.State != ops.Succeeded {
		t.Fatal(op, err)
	}
	latest, err := db.LatestSecretVersion(context.Background(), "hello", "hello-token")
	if err != nil || latest != 1 {
		t.Fatal("reservation missing", latest, err)
	}
}

type failedIntent struct{ Store }

func (s failedIntent) AppendEvent(ctx context.Context, id string, e ops.Event) (uint64, error) {
	if e.Kind == "secret_version" {
		canceled, cancel := context.WithCancel(ctx)
		cancel()
		return s.Store.AppendEvent(canceled, id, e)
	}
	return s.Store.AppendEvent(ctx, id, e)
}
func TestFailedIntentLeavesNoReservationOrRuntimeCreate(t *testing.T) {
	s, db, f := secretFixture(t)
	s.Store = failedIntent{s.Store}
	if _, err := s.Set(context.Background(), "hello", "hello-token", "failed-intent", []byte("PRIVATE")); err == nil {
		t.Fatal("intent failure accepted")
	}
	latest, err := db.LatestSecretVersion(context.Background(), "hello", "hello-token")
	if err != nil || latest != 0 {
		t.Fatal("failed journal reserved a name", latest, err)
	}
	for _, call := range f.calls {
		if call.Args[1] == "create" {
			t.Fatal("created before reservation commit")
		}
	}
	s.Store = db
	got, err := s.Set(context.Background(), "hello", "hello-token", "next-intent", []byte("PRIVATE"))
	if err != nil || got.VersionName != "brine.hello.hello-token.v1" {
		t.Fatal("uncommitted reservation consumed version", got, err)
	}
}
