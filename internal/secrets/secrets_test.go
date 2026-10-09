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
	fake := &fakePodman{}
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
	if got.VersionName != "brine-hello-hello-token-v1" {
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
	const n = 24
	var wg sync.WaitGroup
	fail := make(chan error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, err := s.Set(context.Background(), "hello", "hello-token", fmt.Sprintf("request%d", i), []byte("PRIVATE"))
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
		if !names["brine-hello-hello-token-v"+strconv.Itoa(i)] {
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
