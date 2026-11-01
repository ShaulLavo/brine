//go:build linux

package apply

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/ShaulLavo/brine/internal/ops"
	"github.com/ShaulLavo/brine/internal/plan"
	"github.com/ShaulLavo/brine/internal/policy"
	"github.com/ShaulLavo/brine/internal/store"
	"github.com/ShaulLavo/brine/internal/target"
)

type udpCrashCheckpoint struct {
	Operation                                string
	Plan                                     string
	Facts                                    Facts
	Release                                  Release
	Active, Candidate, HasRelease, Committed bool
}
type udpSQLiteRig struct {
	*udpRig
	database                                      *store.Store
	directory, operation, pauseStep, pauseOutcome string
}

func (r *udpSQLiteRig) GetOperation(ctx context.Context, id string) (Operation, error) {
	return r.database.GetOperation(ctx, id)
}
func (r *udpSQLiteRig) SetOperationState(ctx context.Context, id string, state State) error {
	if err := r.database.SetOperationState(ctx, id, state); err != nil {
		return err
	}
	return r.rig.SetOperationState(ctx, id, state)
}
func (r *udpSQLiteRig) AppendEvent(ctx context.Context, id string, event Event) (uint64, error) {
	sequence, err := r.database.AppendEvent(ctx, id, event)
	if err != nil {
		return 0, err
	}
	if _, err := r.rig.AppendEvent(ctx, id, event); err != nil {
		return 0, err
	}
	var payload stepPayload
	if event.Kind == "step" && json.Unmarshal(event.Payload, &payload) == nil && payload.Step == r.pauseStep && payload.Outcome == r.pauseOutcome {
		if err := r.freeze(); err != nil {
			return 0, err
		}
		fmt.Println("UDP_BOUNDARY_DURABLE")
		time.Sleep(time.Minute)
	}
	return sequence, nil
}
func (r *udpSQLiteRig) freeze() error {
	if r.committed && r.hasRelease {
		observeCommittedRelease(r.rig)
		r.facts.Routing.Generation = r.release.CaddyGeneration
		r.facts.Routing.Files = map[string]string{r.release.CaddyFile.Name: r.release.CaddyFile.Hash}
	}
	checkpoint := udpCrashCheckpoint{Operation: r.operation, Plan: r.plan.Hash, Facts: r.facts, Release: r.release, Active: r.active, Candidate: r.candidate, HasRelease: r.hasRelease, Committed: r.committed}
	raw, err := json.Marshal(checkpoint)
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(r.directory, "checkpoint.json"), raw, 0600)
}
func (r *udpSQLiteRig) CurrentRelease(ctx context.Context, app string) (Release, bool, error) {
	release, err := r.database.CurrentRelease(ctx, app)
	if errors.Is(err, store.ErrNotFound) {
		return Release{}, false, nil
	}
	return release, err == nil, err
}
func (r *udpSQLiteRig) CommitRelease(ctx context.Context, app string, release Release) error {
	if err := r.database.CommitRelease(ctx, app, release); err != nil {
		return err
	}
	if err := r.udpRig.CommitRelease(ctx, app, release); err != nil {
		return err
	}
	return r.pauseApplied("commit")
}
func (r *udpSQLiteRig) RetireApp(ctx context.Context, id, app, release string) error {
	if err := r.database.RetireApp(ctx, id, app, release); err != nil {
		return err
	}
	if err := r.udpRig.RetireApp(ctx, id, app, release); err != nil {
		return err
	}
	return r.pauseApplied("retire_app")
}
func (r *udpSQLiteRig) AppRetired(ctx context.Context, id, app, release string) (bool, error) {
	return r.database.AppRetired(ctx, id, app, release)
}
func (r *udpSQLiteRig) Rollback(ctx context.Context, name, candidate, previous string) error {
	err := r.rig.Rollback(ctx, name, candidate, previous)
	if err == nil && !r.hasRelease {
		r.setLiveUnits([]target.Unit{})
	}
	return err
}
func (r *udpSQLiteRig) wire() {
	r.executor.Journal = r
	r.executor.Releases = r
	r.executor.Plans = r.database
	r.executor.Units = r
	r.executor.UDPReservations = r
	r.executor.UDPChecks = r
	r.executor.Facts = FactsFunc(func(context.Context) (Facts, error) { return r.facts, nil })
}
func (r *udpSQLiteRig) save(t testing.TB) {
	t.Helper()
	if _, err := r.database.SavePlan(t.Context(), r.plan, r.desired); err != nil {
		t.Fatal(err)
	}
	op, _, err := r.database.CreateOperation(t.Context(), ops.Intent{Kind: ops.Deploy, PlanID: r.plan.Hash}, "udp-kill", r.plan.Hash)
	if err != nil {
		t.Fatal(err)
	}
	r.operation = op.ID
}
func newSQLiteUDPRig(t testing.TB, directory, mode string) *udpSQLiteRig {
	t.Helper()
	db, err := store.Open(directory)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	r := &udpSQLiteRig{udpRig: newUDPRig(t, "create"), database: db, directory: directory}
	r.wire()
	if mode == "create" {
		r.save(t)
		return r
	}
	r.save(t)
	if err := r.executor.Run(t.Context(), r.operation, r.plan, r.desired); err != nil {
		t.Fatal("seed", err)
	}
	observeCommittedRelease(r.rig)
	generation, err := db.Generation(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	r.facts.Input.State, err = db.LoadBrineState(t.Context(), r.plan.Target, generation)
	if err != nil {
		t.Fatal(err)
	}
	r.facts.Input.Snapshot.Generation = target.Known(generation)
	r.facts.Routing.Generation = r.release.CaddyGeneration
	r.facts.Routing.Files = map[string]string{r.release.CaddyFile.Name: r.release.CaddyFile.Hash}
	domains := make([]string, 0, len(r.desired.Domains))
	for _, domain := range r.desired.Domains {
		domains = append(domains, string(domain))
	}
	r.facts.Input.Snapshot.LiveCaddyFiles = target.Known([]target.LiveCaddyFile{{Name: r.release.CaddyFile.Name, App: r.plan.App, Domains: target.Known(domains)}})
	a := &(*r.facts.Input.Snapshot.Apps.Value)[0]
	allocation := r.plan.UDP.Next
	a.UDPPublication = target.Known(target.UDPPublication{BindAddress: allocation.Endpoint.BindAddress, HostPort: allocation.Endpoint.HostPort, ContainerPort: allocation.ContainerPort, NetworkMode: "pasta"})
	r.events = nil
	r.effects = nil
	r.state = Queued
	r.committed = false
	r.candidate = false
	if mode == "move" {
		next := *r.desired.UDP
		next.Grant.HostPorts = policy.PortRange{Min: 4434, Max: 4435}
		r.desired.UDP = &next
	}
	r.facts.Input.Desired = r.desired
	if mode == "remove" {
		r.plan, err = plan.BuildRemove(r.facts.Input)
	} else {
		r.plan, err = plan.Build(r.facts.Input)
	}
	if err != nil || r.plan.Kind == plan.Conflict {
		t.Fatal("follow-up", r.plan.Conflicts, err)
	}
	r.save(t)
	return r
}
func TestUDPSubprocessHelper(t *testing.T) {
	directory := os.Getenv("BRINE_UDP_KILL_DIR")
	if directory == "" {
		t.Skip("subprocess entry")
	}
	r := newSQLiteUDPRig(t, directory, os.Getenv("BRINE_UDP_KILL_MODE"))
	r.pauseStep, r.pauseOutcome = os.Getenv("BRINE_UDP_KILL_STEP"), os.Getenv("BRINE_UDP_KILL_OUTCOME")
	if strings.HasPrefix(r.pauseStep, "rollback_") {
		r.failStep = "check_direct"
	}
	if r.pauseStep == "revert_udp" {
		r.failStep = "stage_unit"
	}
	err := r.executor.Run(t.Context(), r.operation, r.plan, r.desired)
	t.Fatalf("did not pause at %s/%s: %v", r.pauseStep, r.pauseOutcome, err)
}
func TestUDPRealSubprocessInterruptionBoundaries(t *testing.T) {
	for _, mode := range []string{"create", "move", "remove"} {
		for _, step := range udpKillSteps(mode) {
			for _, outcome := range []string{"intent", "applied", "completed"} {
				t.Run(mode+"/"+step+"/"+outcome, func(t *testing.T) { runUDPSubprocessBoundary(t, mode, step, outcome) })
			}
		}
	}
}
func udpKillSteps(mode string) []string {
	switch mode {
	case "move":
		return []string{"check_udp_retired"}
	case "remove":
		return []string{"prepare_udp", "check_udp_retired", "retire_app"}
	default:
		return []string{"prepare_udp", "check_udp", "revert_udp", "rollback_udp_check", "rollback_udp", "commit"}
	}
}
func runUDPSubprocessBoundary(t *testing.T, mode, step, outcome string) {
	t.Helper()
	directory := t.TempDir()
	ctx, cancel := context.WithTimeout(t.Context(), 45*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, "/proc/self/exe", "-test.run=^TestUDPSubprocessHelper$")
	command.Env = append(os.Environ(), "BRINE_UDP_KILL_DIR="+directory, "BRINE_UDP_KILL_MODE="+mode, "BRINE_UDP_KILL_STEP="+step, "BRINE_UDP_KILL_OUTCOME="+outcome)
	var stderr bytes.Buffer
	command.Stderr = &stderr
	stdout, err := command.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if command.ProcessState == nil {
			_ = command.Process.Kill()
			_ = command.Wait()
		}
	})
	scanner := bufio.NewScanner(stdout)
	reached := false
	lines := []string{}
	for scanner.Scan() {
		lines = append(lines, scanner.Text())
		if scanner.Text() == "UDP_BOUNDARY_DURABLE" {
			reached = true
			break
		}
	}
	if !reached {
		_ = command.Wait()
		t.Fatalf("boundary not reached: %s; %v", stderr.String(), lines)
	}
	if err := command.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	err = command.Wait()
	var exited *exec.ExitError
	if !errors.As(err, &exited) || exited.Sys().(syscall.WaitStatus).Signal() != syscall.SIGKILL {
		t.Fatal("not a real SIGKILL", err)
	}
	root, err := os.OpenRoot(directory)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = root.Close() }()
	raw, err := root.ReadFile("checkpoint.json")
	if err != nil {
		t.Fatal(err)
	}
	var checkpoint udpCrashCheckpoint
	if err := json.Unmarshal(raw, &checkpoint); err != nil {
		t.Fatal(err)
	}
	db, err := store.Open(directory)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	p, d, err := db.LoadPlan(t.Context(), checkpoint.Plan)
	if err != nil {
		t.Fatal(err)
	}
	op, err := db.GetOperation(t.Context(), checkpoint.Operation)
	if err != nil {
		t.Fatal(err)
	}
	u := newUDPRig(t, "create")
	u.plan, u.desired, u.facts = p, d, checkpoint.Facts
	u.release, u.hasRelease, u.active, u.candidate, u.committed = checkpoint.Release, checkpoint.HasRelease, checkpoint.Active, checkpoint.Candidate, checkpoint.Committed
	u.state = op.State
	generation, err := db.Generation(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	u.facts.Input.State, err = db.LoadBrineState(t.Context(), p.Target, generation)
	if err != nil {
		t.Fatal(err)
	}
	r := &udpSQLiteRig{udpRig: u, database: db, directory: directory, operation: op.ID}
	r.wire()
	events, err := db.EventsAfter(t.Context(), op.ID, 0, 1024)
	if err != nil {
		t.Fatal(err)
	}
	assessment, err := r.executor.InspectRecovery(t.Context(), op, p, d, events)
	if err != nil {
		t.Fatal(err)
	}
	want := RestorePrevious
	if step == "prepare_udp" || mode == "remove" {
		want = ResumeForward
	}
	if step == "commit" {
		want = RequireRecovery
		if outcome != "intent" {
			want = FinishSucceeded
		}
	}
	if step == "retire_app" && outcome != "intent" {
		want = FinishSucceeded
	}
	if assessment.Action != want {
		last, boundary, completed, valid := udpCompensationPrefix(events, p, d)
		x := execution{executor: &r.executor, plan: p}
		hash, known := observedUnitHash(r.facts, p.App, p.App+".container")
		t.Fatalf("action %s, want %s; prefix %v %s %s %v; runtime hash=%q known=%v writer=%v", assessment.Action, want, valid, boundary, last.Step, completed, hash, known, x.inspectWriter(t.Context()))
	}
	if want == RequireRecovery {
		state, err := db.UDPPreparation(t.Context(), op.ID, p)
		if err != nil || state != "prepared" {
			t.Fatal("unknown commit lost ownership", state, err)
		}
		return
	}
	err = r.executor.Recover(t.Context(), assessment)
	if want == ResumeForward || want == FinishSucceeded {
		if err != nil {
			t.Fatal(err)
		}
	}
	final, err := db.GetOperation(t.Context(), op.ID)
	if err != nil {
		t.Fatal(err)
	}
	state, err := db.UDPPreparation(t.Context(), op.ID, p)
	if err != nil || state == "prepared" || state == "unresolved" {
		t.Fatal("terminal left unsettled UDP", final.State, state, err)
	}
	if final.State != Succeeded && final.State != RolledBack && final.State != Failed {
		t.Fatal("not settled", final.State, state)
	}
}

func (r *udpSQLiteRig) pauseApplied(step string) error {
	if r.pauseStep != step || r.pauseOutcome != "applied" {
		return nil
	}
	if err := r.freeze(); err != nil {
		return err
	}
	fmt.Println("UDP_BOUNDARY_DURABLE")
	time.Sleep(time.Minute)
	return nil
}
func (r *udpSQLiteRig) PrepareUDP(ctx context.Context, id string, p plan.Plan) error {
	if err := r.database.PrepareUDP(ctx, id, p); err != nil {
		return err
	}
	return r.pauseApplied("prepare_udp")
}
func (r *udpSQLiteRig) UDPPreparation(ctx context.Context, id string, p plan.Plan) (string, error) {
	return r.database.UDPPreparation(ctx, id, p)
}
func (r *udpSQLiteRig) RevertUDP(ctx context.Context, id string, p plan.Plan) error {
	if err := r.database.RevertUDP(ctx, id, p); err != nil {
		return err
	}
	return r.pauseApplied(r.intent)
}
func (r *udpSQLiteRig) Ready(ctx context.Context, id string, p plan.Plan, previous bool) error {
	if err := r.udpRig.Ready(ctx, id, p, previous); err != nil {
		return err
	}
	return r.pauseApplied(r.intent)
}
func (r *udpSQLiteRig) Absent(ctx context.Context, id string, p plan.Plan, e plan.UDPEndpoint) error {
	if err := r.udpRig.Absent(ctx, id, p, e); err != nil {
		return err
	}
	return r.pauseApplied(r.intent)
}
