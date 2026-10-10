package apply

import (
	"context"
	"crypto/sha256"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/localexec"
	"github.com/ShaulLavo/brine/internal/plan"
	"github.com/ShaulLavo/brine/internal/policy"
)

type preparationFake struct {
	r                  *rig
	calls, inspections int
	prepared           bool
	err                error
}

func (f *preparationFake) PreparePersistent(ctx context.Context, _ string, _ plan.Plan, _ policy.Desired) error {
	f.calls++
	if f.r.intent != "prepare_data" {
		panic("preparation preceded journal intent")
	}
	deadline, ok := ctx.Deadline()
	if !ok || time.Until(deadline) > f.r.executor.effectTimeout() {
		panic("unbounded preparation")
	}
	return f.err
}
func (f *preparationFake) PersistentPrepared(context.Context, string, plan.Plan, policy.Desired) (bool, error) {
	f.inspections++
	return f.prepared, nil
}
func TestPreparePersistentJournaledInterruptionNeverRetries(t *testing.T) {
	x, _, _ := writerExecution(t)
	r := x.executor.Journal.(*rig)
	r.state = Preparing
	x.state = Preparing
	f := &preparationFake{r: r, err: &localexec.Error{Kind: localexec.UnknownOutcome}}
	x.executor.PersistentData = f
	err := x.step(context.Background(), "prepare_data", Preparing, "writer_permit_refused", x.preparePersistent)
	if err == nil || f.calls != 1 || f.inspections != 1 || len(r.events) != 2 {
		t.Fatal("unknown preparation was not inspected once", err, f.calls, f.inspections, r.events)
	}
	if string(r.events[0].Payload) != `{"step":"prepare_data","outcome":"intent"}` || !strings.Contains(string(r.events[1].Payload), `"outcome":"unknown"`) {
		t.Fatal("missing durable unknown boundary", r.events)
	}
	if err = x.fail(context.Background(), err); err == nil || r.state != RecoveryRequired || f.calls != 1 {
		t.Fatal("uncertain preparation retried or settled", err, r.state, f.calls)
	}
}
func TestPreparePersistentRequiresAdapterAndPositiveReadback(t *testing.T) {
	x, _, _ := writerExecution(t)
	if x.preparePersistent(context.Background()) == nil {
		t.Fatal("persistent preparation without adapter")
	}
	f := &preparationFake{r: x.executor.Journal.(*rig), prepared: true}
	x.executor.PersistentData = f
	if x.reconcileUnknown(context.Background(), "prepare_data") != applied || f.inspections != 1 || f.calls != 0 {
		t.Fatal("preparation not inspected read-only")
	}
	f.prepared = false
	if x.reconcileUnknown(context.Background(), "prepare_data") != unresolved {
		t.Fatal("absence authorized replay")
	}
}
func persistentRecoveryRig(t *testing.T) *rig {
	r := newRig(t, false)
	r.desired.Runtime = &data.RuntimeIdentity{UID: 10001, GID: 10001}
	r.desired.Databases = []data.Database{{Name: "main", PersistentRoot: "/srv/data", MountPath: "/data", Filename: "app.db", BackupDestination: "primary", SyncInterval: time.Minute}}
	r.desired.SchemaCompatibility = []data.SchemaCompatibility{{Database: "main", Startup: "preserve", Accepts: []string{data.EmptyMarker}}}
	cadence := data.DefaultBackupCadence()
	r.desired.Backup = &cadence
	r.plan.Runtime = r.desired.Runtime
	r.plan.Backup = r.desired.Backup
	inc := data.AppIncarnationID(strings.Repeat("1", 32))
	db := data.DatabaseID(strings.Repeat("2", 32))
	binding := data.ReplicaBindingID(strings.Repeat("3", 32))
	relative, err := data.RelativeDirectory(inc, db)
	if err != nil {
		t.Fatal(err)
	}
	b := data.DatabaseBinding{DatabaseID: db, IncarnationID: inc, Name: "main", Root: "/srv/data", RelativeDirectory: relative, MountPath: "/data", Filename: "app.db", ReplicaBindingID: binding}
	r.plan.DataMounts = []data.Mount{{Database: b, BindingID: binding, HostPath: filepath.Join(string(b.Root), relative), ContainerPath: b.MountPath}}
	raw, err := r.desired.CanonicalBytes()
	if err != nil {
		t.Fatal(err)
	}
	r.plan.DesiredHash = fmt.Sprintf("sha256:%x", sha256.Sum256(raw))
	r.facts.Input.Desired = r.desired
	r.executor.Facts = FactsFunc(func(context.Context) (Facts, error) { return r.facts, nil })
	return r
}
func TestRecoveryRecognizesPersistentPreparationAndNeverReplaysUnknown(t *testing.T) {
	r := persistentRecoveryRig(t)
	f := &preparationFake{r: r}
	r.executor.PersistentData = f
	events := recoveryEvents("preflight", "pull_image", "verify_image", "ensure_secrets", "prepare_data")
	assessment, err := r.executor.InspectRecovery(context.Background(), Operation{ID: "operation", PlanID: r.plan.Hash, State: Preparing}, r.plan, r.desired, events)
	if err != nil || assessment.Step != "prepare_data" || assessment.Action != RequireRecovery || f.inspections != 1 || f.calls != 0 {
		t.Fatal("preparation boundary not inspected safely", err, assessment, f.inspections, f.calls)
	}
}

func TestPreparePersistentUnknownButProvenCompleteDoesNotReplay(t *testing.T) {
	x, _, _ := writerExecution(t)
	r := x.executor.Journal.(*rig)
	r.state = Preparing
	x.state = Preparing
	f := &preparationFake{r: r, prepared: true, err: &localexec.Error{Kind: localexec.UnknownOutcome}}
	x.executor.PersistentData = f
	if err := x.step(context.Background(), "prepare_data", Preparing, "writer_permit_refused", x.preparePersistent); err != nil {
		t.Fatal(err)
	}
	if f.calls != 1 || f.inspections != 1 || len(r.events) != 3 || !strings.Contains(string(r.events[2].Payload), `"outcome":"completed"`) {
		t.Fatal("proven preparation replayed or not journaled", r.events, f.calls, f.inspections)
	}
}
func TestStatelessStepVocabularyOmitsPreparation(t *testing.T) {
	r := newRig(t, false)
	for _, name := range deploymentSteps(r.desired) {
		if name == "prepare_data" {
			t.Fatal("stateless preparation event")
		}
	}
	x := &execution{executor: &r.executor, desired: r.desired}
	if err := x.preparePersistent(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(r.events) != 0 || len(r.effects) != 0 {
		t.Fatal("stateless preparation had effects")
	}
}
