package apply

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/plan"
	"github.com/ShaulLavo/brine/internal/policy"
	"github.com/ShaulLavo/brine/internal/quadlet"
	"github.com/ShaulLavo/brine/internal/systemd"
	"github.com/ShaulLavo/brine/internal/target"
)

type writerStartsFake struct {
	calls             []string
	bindErr, clearErr error
}

func (f *writerStartsFake) BindWriterStart(_ context.Context, operation string, p plan.Plan, d policy.Desired) error {
	f.calls = append(f.calls, "bind:"+operation)
	if operation == "" || p.App != string(d.Name) {
		return errors.New("bad intent")
	}
	return f.bindErr
}
func (f *writerStartsFake) ClearWriterStart(_ context.Context, operation string) error {
	f.calls = append(f.calls, "clear:"+operation)
	return f.clearErr
}

func writerExecution(t testing.TB) (*execution, *writerStartsFake, *int) {
	t.Helper()
	r := newRig(t, false)
	d := r.desired
	d.Runtime = &data.RuntimeIdentity{UID: 10001, GID: 10001}
	d.Databases = []data.Database{{Name: "main"}}
	unit, err := systemd.ParseUnit("hello.service")
	if err != nil {
		t.Fatal(err)
	}
	starts := 0
	intent := &writerStartsFake{}
	r.executor.Systemd = &systemd.Fake{StartFunc: func(context.Context, systemd.Unit) error {
		if len(intent.calls) == 0 || intent.calls[len(intent.calls)-1] != "bind:operation" {
			t.Fatal("writer effect preceded durable intent")
		}
		starts++
		return nil
	}}
	r.executor.WriterStarts = intent
	x := &execution{executor: &r.executor, id: "operation", desired: d, plan: r.plan, service: unit}
	return x, intent, &starts
}
func TestPersistentWriterStartBindsBeforeEffectAndRefusesMissingIntent(t *testing.T) {
	x, intent, starts := writerExecution(t)
	if err := x.startWriter(context.Background()); err != nil {
		t.Fatal(err)
	}
	if *starts != 1 || !x.started || strings.Join(intent.calls, ",") != "bind:operation" {
		t.Fatal("writer intent not bound", intent.calls, *starts)
	}
	x.executor.WriterStarts = nil
	if err := x.startWriter(context.Background()); err == nil {
		t.Fatal("persistent start without binding adapter")
	}
	if *starts != 1 {
		t.Fatal("missing intent reached writer")
	}
	x.executor.WriterStarts = intent
	intent.bindErr = errors.New("held fence")
	if err := x.startWriter(context.Background()); err == nil {
		t.Fatal("writer start after refused binding")
	}
	if *starts != 1 {
		t.Fatal("fenced intent reached writer")
	}
}
func TestPersistentWriterIntentClearsOnlyAtSettledTerminal(t *testing.T) {
	for _, state := range []State{Succeeded, Failed, RolledBack, RecoveryRequired} {
		t.Run(string(state), func(t *testing.T) {
			x, intent, _ := writerExecution(t)
			journal := x.executor.Journal.(*rig)
			if state == Succeeded {
				journal.state = Committing
			} else if state == RolledBack {
				journal.state = RollingBack
			}
			if err := x.terminal(context.Background(), state, nil); err != nil {
				t.Fatal(err)
			}
			want := 1
			if state == RecoveryRequired {
				want = 0
			}
			if len(intent.calls) != want {
				t.Fatal("wrong settled cleanup", state, intent.calls)
			}
		})
	}
	x, intent, _ := writerExecution(t)
	intent.clearErr = errors.New("clear unknown")
	if err := x.terminal(context.Background(), Succeeded, nil); err == nil {
		t.Fatal("successful terminal after unknown cleanup")
	}
	if x.state == Succeeded {
		t.Fatal("marked settled despite unknown pending intent")
	}
}
func TestPersistentRollbackClearsCandidateBeforeRestoringUnit(t *testing.T) {
	x, intent, _ := writerExecution(t)
	if err := x.clearWriterStart(context.Background()); err != nil {
		t.Fatal(err)
	}
	if strings.Join(intent.calls, ",") != "clear:operation" {
		t.Fatal("pending candidate remains")
	}
	intent.clearErr = errors.New("clear failed")
	if err := x.clearWriterStart(context.Background()); err == nil {
		t.Fatal("unknown clear allowed compensation")
	}
}

type intentRollbackUnits struct {
	Units
	intent *writerStartsFake
	t      testing.TB
}

func (u intentRollbackUnits) Rollback(ctx context.Context, name, candidate, previous string) error {
	if len(u.intent.calls) == 0 || u.intent.calls[len(u.intent.calls)-1] != "clear:operation" {
		u.t.Fatal("candidate intent survived unit compensation")
	}
	return u.Units.Rollback(ctx, name, candidate, previous)
}
func TestPersistentCompensationRefusesWrongRestoredHash(t *testing.T) {
	r := newRig(t, true)
	configureSettledRecoveryWriter(r)
	r.active = false
	r.state = Starting
	u, err := quadlet.Render(r.desired, r.plan, *r.plan.Image.ManifestDigest.Value)
	if err != nil {
		t.Fatal(err)
	}
	r.setLiveUnits([]target.Unit{{Name: u.Name(), Hash: u.Hash()}})
	d := r.desired
	d.Databases = []data.Database{{Name: "main"}}
	intent := &writerStartsFake{calls: []string{"bind:operation"}}
	r.executor.WriterStarts = intent
	r.executor.Units = intentRollbackUnits{Units: r, intent: intent, t: t}
	r.executor.Journal = boundaryRaceJournal{Journal: r, step: "rollback_start", changed: func() { r.setLiveUnits([]target.Unit{{Name: u.Name(), Hash: "sha256:" + strings.Repeat("f", 64)}}) }}
	x := &execution{executor: &r.executor, id: "operation", plan: r.plan, desired: d, previous: r.release, previousDesired: r.oldDesired, hasPrevious: true, unit: u, service: mustUnit(t), facts: r.facts, installed: true, quiesced: true, state: Starting}
	err = x.fail(context.Background(), &Error{Step: "start_unit", Code: "writer_permit_refused"})
	if err == nil || r.state != RecoveryRequired || r.active {
		t.Fatal("wrong restored hash started", err, r.state, r.active)
	}
	for _, effect := range r.effects {
		if effect == "rollback_start" {
			t.Fatal("unsafe compensation start executed")
		}
	}
	if len(intent.calls) != 2 || intent.calls[1] != "clear:operation" {
		t.Fatal("intent was not cleared before restore", intent.calls)
	}
}
func mustUnit(t testing.TB) systemd.Unit {
	t.Helper()
	u, err := systemd.ParseUnit("hello.service")
	if err != nil {
		t.Fatal(err)
	}
	return u
}
