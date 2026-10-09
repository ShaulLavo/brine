package transport

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ShaulLavo/brine/internal/apps"
	"github.com/ShaulLavo/brine/internal/dispatch"
	"github.com/ShaulLavo/brine/internal/localexec"
	"github.com/ShaulLavo/brine/internal/plan"
	"github.com/ShaulLavo/brine/internal/podman"
	"github.com/ShaulLavo/brine/internal/policy"
	"github.com/ShaulLavo/brine/internal/secrets"
	"github.com/ShaulLavo/brine/internal/store"
)

type configStub struct{ edits []apps.Edit }

func (s *configStub) ConfigSet(_ context.Context, _ string, edits []apps.Edit) (apps.ConfigPlan, error) {
	s.edits = edits
	return apps.ConfigPlan{PlanID: "sha256:" + strings.Repeat("a", 64), Kind: plan.Update, Conflicts: []plan.Diagnostic{}}, nil
}
func (s *configStub) Lifecycle(_ context.Context, _ string, action plan.ChangeKind) (apps.ConfigPlan, error) {
	return apps.ConfigPlan{Lifecycle: action, PlanID: "sha256:" + strings.Repeat("b", 64), Kind: plan.Update, Conflicts: []plan.Diagnostic{}}, nil
}

type secretRuntime struct{ calls []localexec.Command }

func (s *secretRuntime) Execute(_ context.Context, c localexec.Command) (localexec.Result, error) {
	copy := c
	copy.Stdin = append([]byte(nil), c.Stdin...)
	s.calls = append(s.calls, copy)
	if c.Args[1] == "ls" {
		return localexec.Result{}, nil
	}
	if c.Args[1] == "create" {
		return localexec.Result{Stdout: "immutable-id"}, nil
	}
	return localexec.Result{}, &localexec.Error{Kind: localexec.Failed, ExitCode: 1}
}

func TestConfigurationAndBootstrapSecretThroughRestrictedTransport(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	db, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	raw, err := os.ReadFile("../policy/testdata/operator.toml")
	if err != nil {
		t.Fatal(err)
	}
	pol, err := policy.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	runtime := &secretRuntime{}
	session, err := localexec.NewSession(runtime, 1000, dir, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	service := secrets.Service{Store: db, Podman: podman.New(session), Requester: "agent", LoadPolicy: func(context.Context) (policy.Policy, error) { return pol, nil }}
	config := &configStub{}
	server := dispatch.NewServer("fixture", nil).WithJobs(nil, func(context.Context, dispatch.Class) error { return nil })
	server.Config = config
	server.Secrets = service
	runner := &dispatcherSSH{server: server}
	client := Client{Runner: runner, KnownHostsDir: filepath.Join(dir, "pins"), LookPath: func(string) (string, error) { return "/fixture/ssh", nil }}
	call := func(op string, args any) any {
		t.Helper()
		raw, err := json.Marshal(args)
		if err != nil {
			t.Fatal(err)
		}
		response, err := client.Call(ctx, validTarget(), dispatch.Request{SchemaVersion: 1, Op: op, RequestID: op + "-request", Args: raw})
		if err != nil || !response.OK {
			t.Fatal(response, err)
		}
		encoded, _ := json.Marshal(response)
		if strings.Contains(string(encoded), "PLANTED_PRIVATE_VALUE") {
			t.Fatal("response leaked private input")
		}
		return response.Data
	}
	edits := []apps.Edit{{Key: "environment.TOKEN", Value: "PLANTED_PRIVATE_VALUE", Action: ""}}
	if got, ok := call("config_set", dispatch.ConfigArgs{App: "hello", Edits: edits}).(apps.ConfigPlan); !ok || got.Kind != plan.Update || config.edits[0].Value != edits[0].Value {
		t.Fatal(got)
	}
	for _, action := range []plan.ChangeKind{plan.StartApp, plan.StopApp, plan.RestartApp} {
		if got := call("lifecycle", dispatch.LifecycleArgs{App: "hello", Action: action}).(apps.ConfigPlan); got.Lifecycle != action {
			t.Fatal(got)
		}
	}
	got := call("secret_set", dispatch.SecretArgs{App: "hello", Reference: "hello-token", Value: []byte("PLANTED_PRIVATE_VALUE")}).(secrets.Stored)
	if got.Bound || got.VersionName != "brine.hello.hello-token.v1" {
		t.Fatal(got)
	}
	events, err := db.EventsAfter(ctx, got.OperationID, 0, 128)
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(events)
	if strings.Contains(string(encoded), "PLANTED_PRIVATE_VALUE") {
		t.Fatal("audit leaked")
	}
	for _, c := range runtime.calls {
		if strings.Contains(strings.Join(c.Args, " ")+strings.Join(c.Env, " "), "PLANTED_PRIVATE_VALUE") {
			t.Fatal("secret not stdin-only")
		}
		if c.Args[1] == "create" && string(c.Stdin) != "PLANTED_PRIVATE_VALUE" {
			t.Fatal("stdin changed")
		}
	}
}
