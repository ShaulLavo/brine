package transport

import (
	"bytes"
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/ShaulLavo/brine/internal/dispatch"
	"github.com/ShaulLavo/brine/internal/localexec"
	"github.com/ShaulLavo/brine/internal/policy"
	"github.com/ShaulLavo/brine/internal/readiness"
)

type reviewPolicy struct{ value policy.Policy }

func (p reviewPolicy) Load(context.Context) (policy.Policy, error) { return p.value, nil }

type reviewReadinessRunner struct {
	t        *testing.T
	p        policy.Policy
	calls    int
	deadline time.Time
}

func (r *reviewReadinessRunner) RunInput(ctx context.Context, c localexec.Command) (localexec.Output, error) {
	r.calls++
	r.deadline, _ = ctx.Deadline()
	request, err := dispatch.DecodeRequest(c.Stdin)
	if err != nil || request.Op != "readiness" || string(request.Args) != "{}" {
		r.t.Fatal(request, err)
	}
	server := dispatch.NewServer("fixture", nil)
	server.Readiness = readiness.Reader{Policy: reviewPolicy{r.p}}
	response, err := server.Handle(ctx, bytes.NewReader(c.Stdin))
	if err != nil {
		r.t.Fatal(err)
	}
	raw, err := json.Marshal(response)
	return localexec.Output{Stdout: raw}, err
}
func TestReviewReadinessActualTransportBoundary(t *testing.T) {
	for _, wildcard := range []bool{false, true} {
		reviewReadinessTransportCase(t, wildcard)
	}
}
func reviewReadinessTransportCase(t *testing.T, wildcard bool) {
	t.Helper()
	domains := `["app.example.com"]`
	if wildcard {
		domains = `["*.example.com"]`
	}
	p, err := policy.Parse([]byte("schema_version=1\nversion=\"fixture\"\nallowed_domains=" + domains + "\n[resources]\nmemory_mb=512\npids_limit=128\n"))
	if err != nil {
		t.Fatal(err)
	}
	runner := &reviewReadinessRunner{t: t, p: p}
	c := Client{KnownHostsDir: filepath.Join(t.TempDir(), "pins"), Runner: runner, LookPath: func(string) (string, error) { return "/fixture/ssh", nil }}
	start := time.Now()
	response, err := c.Call(t.Context(), validTarget(), dispatch.Request{SchemaVersion: 1, Op: "readiness", RequestID: "fixture", Args: json.RawMessage(`{}`)})
	if runner.calls != 1 {
		t.Fatal("retried readiness")
	}
	if runner.deadline.Before(start.Add(14*time.Second)) || runner.deadline.After(start.Add(16*time.Second)) {
		t.Fatal("unbounded readiness transport")
	}
	t.Logf("wildcard=%t ok=%t err=%v", wildcard, response.OK, err)
	if err != nil || !response.OK {
		t.Error("supported policy did not survive actual dispatcher/transport decoding")
	}
}
