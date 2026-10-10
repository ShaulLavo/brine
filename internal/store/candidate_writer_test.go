//go:build linux

package store

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/ops"
	"github.com/ShaulLavo/brine/internal/plan"
	"github.com/ShaulLavo/brine/internal/policy"
)

func candidateFixture(t *testing.T, s *Store) (ReservedDatabase, policy.Desired, plan.Plan) {
	t.Helper()
	req := dataRequest()
	req.App = "hello"
	reserved, err := s.ReserveDatabase(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	in := fixture(t)
	in.Desired.Runtime = &data.RuntimeIdentity{UID: 10001, GID: 10001}
	in.Desired.Databases = []data.Database{req.Database}
	in.Desired.SchemaCompatibility = []data.SchemaCompatibility{{Database: "main", Startup: "preserve", Accepts: []string{data.EmptyMarker}}}
	p, err := plan.Build(in)
	if err != nil {
		t.Fatal(err)
	}
	// Synthetic stored candidate for isolated state-machine tests; not a deploy.
	p.Kind = plan.Create
	p.DataMounts = []data.Mount{reserved.Mount()}
	if _, err = s.SavePlan(context.Background(), p, in.Desired); err != nil {
		t.Fatal(err)
	}
	return reserved, in.Desired, p
}
func TestCandidateSchemaBeforeFirstCommittedRelease(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	reserved, desired, _ := candidateFixture(t, s)
	receipt := data.AllocationReceipt{DatabaseID: reserved.Database.DatabaseID, IncarnationID: reserved.Database.IncarnationID, RootDevice: 1, RootInode: 2, DirectoryDevice: 1, DirectoryInode: 3, AllocatedAt: time.Now().UTC()}
	if err := s.RecordAllocation(ctx, receipt); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordAllocation(ctx, receipt); err != nil {
		t.Fatal("idempotent receipt", err)
	}
	ro, err := OpenReadOnly(ctx, s.dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ro.Close() }()
	schema, err := ro.CandidateWriterSchema(ctx, reserved.Database.IncarnationID, desired)
	if err != nil {
		t.Fatal(err)
	}
	if schema.ReleaseID != "" || len(schema.Bindings) != 1 || len(schema.Allocations) != 1 || len(schema.Definitions) != 1 {
		t.Fatalf("candidate evidence: %+v", schema)
	}
	if err = s.RecordWriterAttempt(ctx, reserved.Database.IncarnationID, "attempt-1"); err != nil {
		t.Fatal(err)
	}
	schema, err = ro.CandidateWriterSchema(ctx, reserved.Database.IncarnationID, desired)
	if err != nil {
		t.Fatal(err)
	}
	if len(schema.Allocations) != 0 {
		t.Fatal("writer history retained untouched empty proof")
	}
	if err = s.RecordAllocation(ctx, receipt); !errors.Is(err, ErrConflict) {
		t.Fatal("history repaired by replacement allocation", err)
	}
	if _, err = s.HoldDataFence(ctx, reserved.Database.DatabaseID, "fence-owner"); err != nil {
		t.Fatal(err)
	}
	if _, err = ro.CandidateWriterSchema(ctx, reserved.Database.IncarnationID, desired); !errors.Is(err, ErrConflict) {
		t.Fatal("candidate bypassed held fence", err)
	}
}
func TestWriterStartResolutionThreeStates(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	reserved, _, p := candidateFixture(t, s)
	incarnation := reserved.Database.IncarnationID
	ro, err := OpenReadOnly(ctx, s.dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ro.Close() }()
	resolution, err := ro.ReadWriterStart(ctx, incarnation)
	if err != nil || resolution.State != WriterStartNone {
		t.Fatalf("no intent: %+v %v", resolution, err)
	}
	op, _, err := s.CreateOperation(ctx, ops.Intent{Kind: ops.Deploy, PlanID: p.Hash}, "requester", "writer-start")
	if err != nil {
		t.Fatal(err)
	}
	if err = s.BindWriterStart(ctx, op.ID, p.Hash, incarnation, p.DesiredHash); !errors.Is(err, ErrConflict) {
		t.Fatal("queued intent allowed", err)
	}
	for _, state := range []ops.State{ops.Preflight, ops.Preparing, ops.Starting} {
		if err = s.SetOperationState(ctx, op.ID, state); err != nil {
			t.Fatal(err)
		}
	}
	if err = s.BindWriterStart(ctx, op.ID, p.Hash, incarnation, p.DesiredHash); err != nil {
		t.Fatal(err)
	}
	if err = s.BindWriterStart(ctx, op.ID, p.Hash, incarnation, p.DesiredHash); err != nil {
		t.Fatal("idempotent bind", err)
	}
	resolution, err = ro.ReadWriterStart(ctx, incarnation)
	if err != nil || resolution.State != WriterStartPending || resolution.Intent.OperationID != op.ID {
		t.Fatalf("pending: %+v %v", resolution, err)
	}
	if err = s.SetOperationState(ctx, op.ID, ops.RecoveryRequired); err != nil {
		t.Fatal(err)
	}
	resolution, err = ro.ReadWriterStart(ctx, incarnation)
	if err != nil || resolution.State != WriterStartInvalid {
		t.Fatalf("invalid intent fell back: %+v %v", resolution, err)
	}
	if err = s.ClearWriterStart(ctx, op.ID); err != nil {
		t.Fatal(err)
	}
	resolution, err = ro.ReadWriterStart(ctx, incarnation)
	if err != nil || resolution.State != WriterStartNone {
		t.Fatalf("settled intent: %+v %v", resolution, err)
	}
}
