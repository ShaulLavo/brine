//go:build linux

package host

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ShaulLavo/brine/internal/apps"
	"github.com/ShaulLavo/brine/internal/archive"
	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/dispatch"
	"github.com/ShaulLavo/brine/internal/jobs"
	"github.com/ShaulLavo/brine/internal/localexec"
	"github.com/ShaulLavo/brine/internal/ops"
	"github.com/ShaulLavo/brine/internal/plan"
	"github.com/ShaulLavo/brine/internal/result"
	"github.com/ShaulLavo/brine/internal/target"
	"github.com/ShaulLavo/brine/internal/transport"
)

type removalWire struct{ factory *ServerFactory }

func (w removalWire) RunInput(ctx context.Context, c localexec.Command) (localexec.Output, error) {
	server := dispatch.NewServer("fixture", nil)
	server.Factory = w.factory.Build
	response, runErr := server.Handle(ctx, bytes.NewReader(c.Stdin))
	raw, err := json.Marshal(response)
	if err != nil {
		return localexec.Output{}, err
	}
	if runErr != nil {
		command := exec.CommandContext(ctx, "/bin/sh", "-c", `exit "$BRINE_TEST_EXIT"`)
		command.Env = []string{"BRINE_TEST_EXIT=" + strconv.Itoa(result.ExitCode(runErr))}
		runErr = command.Run()
	}
	return localexec.Output{Stdout: raw}, runErr
}

type connectedRemovalRig struct {
	source  *removalHostRig
	runtime *Runtime
	client  transport.Client
	target  transport.Target
}

func connectRemovalRig(t *testing.T, r *removalHostRig) *connectedRemovalRig {
	t.Helper()
	tasks := jobs.Service{Store: r.prep.state, Requester: sqlitePrepRequester, Launcher: &fakeLaunch{}}
	app := apps.Service{Store: r.prep.state, Inventory: r.inventory, LoadPolicy: r.host.service.Policy.Load}
	runtime := &Runtime{Inventory: r.inventory, Runner: jobs.Runner{Store: r.prep.state, TaskHandlers: map[ops.Kind]jobs.TaskHandler{}}, Authorize: func(context.Context, dispatch.Class) error { return nil }, close: func() error { return nil }}
	wireSQLiteRemoval(runtime, app, r.service, tasks, r.prep.state)
	factory := newServerFactory("fixture", "deploy", func(context.Context, string) (*Runtime, error) { return runtime, nil }, nil)
	factory.diagnosticStateDir = r.prep.root
	t.Cleanup(func() { _ = factory.Close() })
	blob := append([]byte("\x00\x00\x00\x0bssh-ed25519\x00\x00\x00\x20"), bytes.Repeat([]byte("x"), 32)...)
	target := transport.Target{Name: "fixture", Destination: "runner@fixture.invalid", IdentityPath: "/fixture/identity", PinnedHostKey: "ssh-ed25519 " + base64.StdEncoding.EncodeToString(blob)}
	client := transport.Client{KnownHostsDir: privateRemovalDirectory(t), Runner: removalWire{factory}, LookPath: func(string) (string, error) { return "/fixture/ssh", nil }}
	return &connectedRemovalRig{source: r, runtime: runtime, client: client, target: target}
}
func (r *connectedRemovalRig) response(t *testing.T, op string, args any) (result.Envelope, error) {
	t.Helper()
	raw, err := json.Marshal(args)
	if err != nil {
		t.Fatal(err)
	}
	return r.client.Call(t.Context(), r.target, dispatch.Request{SchemaVersion: 1, Op: op, RequestID: "removal-fixture", Args: raw})
}
func (r *connectedRemovalRig) call(t *testing.T, op string, args any) result.Envelope {
	t.Helper()
	response, err := r.response(t, op, args)
	if err != nil || !response.OK {
		t.Fatal(op, response.Error, err)
	}
	return response
}
func (r *connectedRemovalRig) plan(t *testing.T) apps.ConfigPlan {
	t.Helper()
	p := r.call(t, "lifecycle", dispatch.LifecycleArgs{App: "hello", Action: plan.RemoveApp}).Data.(apps.ConfigPlan)
	if p.Archive == nil || !p.Archive.Valid() || p.Kind != plan.Update || p.Lifecycle != plan.RemoveApp || p.PlanID == "" {
		t.Fatal(p)
	}
	if r.source.recorder.inventory.removed {
		t.Fatal("planning changed runtime")
	}
	return p
}
func (r *connectedRemovalRig) apply(t *testing.T, p apps.ConfigPlan) jobs.Accepted {
	t.Helper()
	accepted := r.call(t, "apply", dispatch.ApplyArgs{PlanID: p.PlanID, IdempotencyKey: r.source.prep.key()}).Data.(jobs.Accepted)
	op, err := r.source.prep.state.GetOperation(t.Context(), accepted.OperationID)
	if err != nil || op.Kind != ops.SQLiteRetire || op.PlanID != "" {
		t.Fatal("apply took deployment path", op, err)
	}
	return accepted
}
func (r *connectedRemovalRig) status(t *testing.T, id string) jobs.Status {
	t.Helper()
	return r.call(t, "operation", dispatch.OperationArgs{OperationID: id}).Data.(jobs.Status)
}
func TestSQLiteRetireConnectedReleased(t *testing.T) {
	r := connectRemovalRig(t, newUnclaimedRemovalHostRigSource(t, false, true))
	p := r.plan(t)
	if p.Archive.Databases[0].Arm != "prepared_ltx" {
		t.Fatal("released point is not LTX", p.Archive)
	}
	puts, gets := r.source.prep.remote.counts()
	accepted := r.apply(t, p)
	if err := r.runtime.Runner.Run(t.Context(), accepted.OperationID); err != nil {
		t.Fatal("connected runner", err)
	}
	status := r.status(t, accepted.OperationID)
	if status.Operation.State != ops.Succeeded || status.Outcome == nil {
		t.Fatal(status)
	}
	receipt, err := archive.DecodeRemovalReceipt(status.Outcome.Receipt)
	if err != nil || receipt.PlanID != p.PlanID || receipt.ArchiveID != p.Archive.ArchiveID || receipt.Outcome != "retired" {
		t.Fatal(receipt, err)
	}
	stored, err := r.source.prep.state.LoadArchivePlan(t.Context(), p.PlanID)
	if err != nil || data.VerifyArchivedTree(stored.Trees[0], stored.Candidate.Incarnation, stored.ArchiveID) != nil {
		t.Fatal("archive absent", err)
	}
	afterPuts, afterGets := r.source.prep.remote.counts()
	if puts != afterPuts || gets != afterGets {
		t.Fatal("retirement ran a second coverage engine")
	}
	r.source.noWriterEffects(t)
	for _, call := range r.source.recorder.calls {
		if strings.Contains(call, " start ") || strings.Contains(call, " restart ") {
			t.Fatal(call)
		}
	}
}

func TestSQLiteRetireConnectedEmptyAndMultipleDatabases(t *testing.T) {
	for _, name := range []string{"empty", "empty-two", "written-two", "allocated-point", "initialized"} {
		t.Run(name, func(t *testing.T) { testSQLiteRetireConnectedVariant(t, name) })
	}
}
func testSQLiteRetireConnectedVariant(t *testing.T, name string) {
	r := connectedRemovalVariant(t, name)
	p := r.plan(t)
	expected := 1
	if strings.HasSuffix(name, "-two") {
		expected = 2
	}
	if len(p.Archive.Databases) != expected {
		t.Fatal("database summary", p.Archive)
	}
	accepted := r.apply(t, p)
	if err := r.runtime.Runner.Run(t.Context(), accepted.OperationID); err != nil {
		t.Fatal("connected runner", err)
	}
	status := r.status(t, accepted.OperationID)
	if status.Operation.State != ops.Succeeded || status.Outcome == nil {
		t.Fatal(status)
	}
	receipt, err := archive.DecodeRemovalReceipt(status.Outcome.Receipt)
	if err != nil || receipt.Outcome != "retired" || receipt.ArchiveID != p.Archive.ArchiveID {
		t.Fatal(receipt, err)
	}
	r.source.noWriterEffects(t)
}
func connectedRemovalVariant(t *testing.T, name string) *connectedRemovalRig {
	t.Helper()
	switch name {
	case "empty", "empty-two":
		r := newUnclaimedEmptyRemovalHostRig(t, name == "empty-two")
		raw, err := os.ReadFile("../target/testdata/ready-arm64.json")
		if err != nil {
			t.Fatal(err)
		}
		snapshot, err := target.Decode(raw)
		if err != nil {
			t.Fatal(err)
		}
		r.inventory.snapshot = snapshot
		return connectRemovalRig(t, r)
	case "written-two":
		return connectRemovalRig(t, newUnclaimedRemovalHostRigSource(t, true, false))
	default:
		u := newUnreleasedSQLiteRig(t)
		if name == "initialized" {
			u.initialize(t)
		}
		_, receipt := u.prepare(t, "")
		if receipt == nil {
			t.Fatal("genuine point missing")
		}
		return connectUnreleasedRemoval(t, u)
	}
}
func connectUnreleasedRemoval(t *testing.T, u *unreleasedSQLiteRig) *connectedRemovalRig {
	t.Helper()
	prep := u.physical
	inv := &removalInventory{state: prep.state, snapshot: u.deploy.inventory.snapshot}
	recorder := &removalRecorder{prep: prep.exec, inventory: inv}
	session, err := localexec.NewSession(recorder, 1000, "/", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	s := newArchiveService(u.deploy.service, prep.root, prep.home, session, recorder, recorder, recorder)
	h := s.Host.(archiveHost)
	h.service.Inventory = inv
	h.restoreTesting = &archiveRestoreTesting{cli: restoreTaskCLI{source: prep.source}, endpoint: prep.remote.server.URL}
	s.Host = h
	r := &removalHostRig{prep: prep, host: h, service: s, inventory: inv, recorder: recorder}
	return connectRemovalRig(t, r)
}

func TestSQLiteRetireZeroPointRetainsAbsentBundleContract(t *testing.T) {
	r := newUnclaimedEmptyRemovalHostRig(t, false)
	hold, err := r.host.HoldPrepared(t.Context(), r.plan)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = hold.Close() })
	d, _ := r.plan.Candidate.Databases[0].SQLite()
	path := filepath.Join(string(d.Binding.Root), d.Binding.RelativeDirectory, "unrecognized")
	if err = os.WriteFile(path, []byte("not SQLite"), 0600); err != nil {
		t.Fatal(err)
	}
	if err = hold.Recheck(t.Context()); err == nil {
		t.Fatal("unknown member accepted in zero-point bundle")
	}
	if err = os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err = hold.Recheck(t.Context()); err != nil {
		t.Fatal("fresh census failed", err)
	}
}
