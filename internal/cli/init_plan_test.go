package cli

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ShaulLavo/brine/internal/dispatch"
	"github.com/ShaulLavo/brine/internal/host"
	"github.com/ShaulLavo/brine/internal/plan"
	"github.com/ShaulLavo/brine/internal/policy"
	"github.com/ShaulLavo/brine/internal/result"
	"github.com/ShaulLavo/brine/internal/spec"
	"github.com/ShaulLavo/brine/internal/store"
	"github.com/ShaulLavo/brine/internal/target"
	"github.com/ShaulLavo/brine/internal/transport"
)

type initInventory struct{ snapshot target.Snapshot }

func (i initInventory) Collect(ctx context.Context) (target.Snapshot, error) {
	return i.snapshot, ctx.Err()
}

type initPolicy struct{ policy policy.Policy }

func (p initPolicy) Load(ctx context.Context) (policy.Policy, error) { return p.policy, ctx.Err() }

type initRegistryTransport struct {
	t      *testing.T
	values map[string][]byte
	calls  int
}

func (r *initRegistryTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	r.calls++
	if request.Method != http.MethodGet || request.URL.Host != "registry.example.com:5000" || !strings.HasPrefix(request.URL.Path, "/v2/team/hello/") || request.Header.Get("Authorization") != "" {
		r.t.Fatalf("unexpected registry request %v", request)
	}
	raw, ok := r.values[filepath.Base(request.URL.Path)]
	if !ok {
		r.t.Fatalf("unbounded registry request %v", request.URL)
	}
	return &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: io.NopCloser(bytes.NewReader(raw))}, nil
}

func initFixtureHash(raw []byte) string { return fmt.Sprintf("sha256:%x", sha256.Sum256(raw)) }

func TestInitGeneratedSpecPlans(t *testing.T) {
	state, err := store.OpenContext(t.Context(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := state.Close(); err != nil {
			t.Error(err)
		}
	})
	snapshotRaw, err := os.ReadFile("../target/testdata/ready-arm64.json")
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := target.Decode(snapshotRaw)
	if err != nil {
		t.Fatal(err)
	}
	polRaw, err := os.ReadFile("../policy/testdata/operator.toml")
	if err != nil {
		t.Fatal(err)
	}
	pol, err := policy.Parse(polRaw)
	if err != nil {
		t.Fatal(err)
	}
	config := []byte(`{"os":"linux","architecture":"arm64"}`)
	manifest := []byte(`{"schemaVersion":2,"config":{"digest":"` + initFixtureHash(config) + `"}}`)
	registry := &initRegistryTransport{t: t, values: map[string][]byte{initFixtureHash(config): config, initFixtureHash(manifest): manifest}}
	service := host.Service{Store: state, Inventory: initInventory{snapshot}, Policy: initPolicy{pol}, Images: host.Registry{Transport: registry}, Requester: "init-fixture"}
	server := dispatch.NewServer("fixture", service.Inventory).WithJobs(nil, service.Authorize)
	server.Planner = service
	checkInitPlanner(t, service, server, registry, initFixtureHash(manifest))
}

func checkInitPlanner(t *testing.T, service host.Service, server *dispatch.Server, registry *initRegistryTransport, digest string) {
	t.Helper()
	dir := t.TempDir()
	args := initArgs(dir)
	args[5] = "registry.example.com:5000/team/hello@" + digest
	var out, diagnostics bytes.Buffer
	deps := initNoHostDependencies(t, &out, &diagnostics)
	if err := Execute(deps, append(args, "--yes", "--json")); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, initFileName)
	raw, err := readInitTestFile(t, dir, initFileName)
	if err != nil {
		t.Fatal(err)
	}
	app, err := spec.Parse(raw)
	if err != nil || app.Health != (spec.Health{Path: "/", ExpectedStatus: 200, StartupDeadlineSeconds: 30, TimeoutSeconds: 3}) || string(app.Image) != args[5] {
		t.Fatal("generated spec", app, err)
	}
	deps.LoadOperationTarget = func(string, string) (transport.Target, error) { return transport.Target{Name: "fixture"}, nil }
	deps.OperationClient = callFunc(func(ctx context.Context, _ transport.Target, request dispatch.Request) (result.Envelope, error) {
		return initPlannerCall(ctx, t, server, request)
	})
	out.Reset()
	if err := Execute(deps, []string{"plan", path, "--target", "fixture", "--json", "--no-input"}); err != nil {
		t.Fatal("real connected plan", err, diagnostics.String())
	}
	var response struct {
		Data dispatch.Planned `json:"data"`
	}
	if err := json.Unmarshal(out.Bytes(), &response); err != nil || response.Data.Kind != plan.Create || len(response.Data.Conflicts) != 0 || response.Data.PlanID == "" || registry.calls != 2 {
		t.Fatalf("plan = %s, %v, registry requests = %d", out.String(), err, registry.calls)
	}
	stored, _, err := service.Store.LoadPlan(t.Context(), store.PlanID(response.Data.PlanID))
	if err != nil || stored.Lifecycle != "" {
		t.Fatal("stored plan", err)
	}
	polRaw, err := os.ReadFile("../policy/testdata/operator.toml")
	if err != nil {
		t.Fatal(err)
	}
	pol, err := policy.Parse(bytes.ReplaceAll(polRaw, []byte("*.Example.com"), []byte("elsewhere.example.net")))
	if err != nil {
		t.Fatal(err)
	}
	service.Policy = initPolicy{pol}
	server.Planner = service
	out.Reset()
	if err := Execute(deps, []string{"plan", path, "--target", "fixture", "--json"}); result.ExitCode(err) != 4 {
		t.Fatal("generated file bypassed host policy", err, out.String())
	}
	if registry.calls != 2 {
		t.Fatal("policy refusal reached registry")
	}
	assertInitFiles(t, dir, []string{initFileName})
}

func initPlannerCall(ctx context.Context, t *testing.T, server *dispatch.Server, request dispatch.Request) (result.Envelope, error) {
	t.Helper()
	if request.Op != "plan" {
		t.Fatalf("unexpected host operation %s", request.Op)
	}
	raw, err := dispatch.EncodeRequest(request)
	if err != nil {
		t.Fatal(err)
	}
	response, err := server.Handle(ctx, bytes.NewReader(raw))
	if err != nil {
		return response, err
	}
	encoded, err := json.Marshal(response)
	if err != nil {
		t.Fatal(err)
	}
	return dispatch.DecodeResponse(encoded, request.Op)
}
