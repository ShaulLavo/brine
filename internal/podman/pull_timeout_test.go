package podman

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/ShaulLavo/brine/internal/localexec"
)

func TestPullSessionDoesNotChangeOtherCommandBudgets(t *testing.T) {
	r := &recorder{results: []localexec.Result{
		{}, {}, {Stdout: fixture(t, "image-inspect.json")},
		{Stdout: fixture(t, "manifest-inspect.json")}, {Stdout: fixture(t, "platform-image-inspect.json")},
	}}
	session, err := localexec.NewSession(r, 1234, t.TempDir(), time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	pull, err := session.WithTimeout(PullTimeout)
	if err != nil {
		t.Fatal(err)
	}
	c := NewWithPullSession(session, pull)
	if err := c.Pull(t.Context(), image(t)); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Inspect(t.Context(), image(t)); err != nil {
		t.Fatal(err)
	}
	if err := c.CreateSecret(t.Context(), name(t), []byte("fixture")); err != nil {
		t.Fatal(err)
	}
	if _, err := c.SecretExists(t.Context(), name(t)); err != nil {
		t.Fatal(err)
	}
	for _, command := range r.commands {
		checkCommandBudget(t, command)
	}
}

func checkCommandBudget(t *testing.T, command localexec.Command) {
	t.Helper()
	want := time.Minute
	if command.Args[0] == "pull" {
		want = 15 * time.Minute
	}
	if command.Timeout != want {
		t.Errorf("%s command timeout = %s, want %s", command.Args[0], command.Timeout, want)
	}
}

type canceledPull struct {
	cancel   context.CancelFunc
	deadline time.Time
	command  localexec.Command
}

func (e *canceledPull) Execute(ctx context.Context, command localexec.Command) (localexec.Result, error) {
	e.deadline, _ = ctx.Deadline()
	e.command = command
	e.cancel()
	return localexec.Result{}, ctx.Err()
}

func TestPullSessionPreservesEarlierParentAndUnknownOutcome(t *testing.T) {
	deadline := time.Now().Add(time.Second)
	parent, cancel := context.WithDeadline(t.Context(), deadline)
	defer cancel()
	r := &canceledPull{cancel: cancel}
	session, err := localexec.NewSession(r, 1234, t.TempDir(), time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	pull, err := session.WithTimeout(PullTimeout)
	if err != nil {
		t.Fatal(err)
	}
	err = NewWithPullSession(session, pull).Pull(parent, image(t))
	var unknown *localexec.Error
	if !errors.As(err, &unknown) || unknown.Kind != localexec.UnknownOutcome {
		t.Fatalf("canceled pull = %v, want unknown outcome", err)
	}
	if !r.deadline.Equal(deadline) || r.command.Timeout != 15*time.Minute {
		t.Fatalf("parent deadline or command cap changed: %v %s", r.deadline, r.command.Timeout)
	}
}

func TestDefaultPullKeepsExplicitCallerSessionBudget(t *testing.T) {
	r := &recorder{}
	session, err := localexec.NewSession(r, 1234, t.TempDir(), 2*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if err := New(session).Pull(t.Context(), image(t)); err != nil {
		t.Fatal(err)
	}
	if len(r.commands) != 1 || r.commands[0].Timeout != 2*time.Minute {
		t.Fatal("explicit two-minute caller budget changed")
	}
}
