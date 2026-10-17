//go:build linux

package store

import (
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/ShaulLavo/brine/internal/cdn"
	"github.com/ShaulLavo/brine/internal/cdn/cdntest"
	"github.com/ShaulLavo/brine/internal/ops"
	"github.com/ShaulLavo/brine/internal/plan"
	"github.com/ShaulLavo/brine/internal/spec"
	"github.com/ShaulLavo/brine/internal/target"
)

func cdnStoreFixture(t *testing.T) (*Store, *cdntest.Fixture, cdn.Attachment) {
	t.Helper()
	s := openTest(t)
	f := cdntest.New(t)
	a := f.Attachment(f.EdgeA.Profile().Reference)
	a.Binding.App = "hello"
	a.SourceRelease = "cdn-source-release"
	a.Owner = cdn.AttachmentFor(a.Binding.Target, "hello", a.SourceRelease)
	a.Revision = cdn.RevisionFor(a, cdn.Hash([]byte("policy")), "")
	in := fixture(t)
	in.Desired.Domains = []spec.Domain{spec.Domain(a.Binding.Hostname)}
	p, err := plan.Build(in)
	if err != nil {
		t.Fatal(err)
	}
	id, err := s.SavePlan(t.Context(), p, in.Desired)
	if err != nil {
		t.Fatal(err)
	}
	r := release(t, s, a.SourceRelease)
	r.PlanID = id
	r.Image = p.Image
	r.HostPort = p.HostPort
	r.Units = r.Units[:1]
	if err = s.CommitRelease(t.Context(), "hello", r); err != nil {
		t.Fatal(err)
	}
	return s, f, a
}

func cdnPlanFixture(t *testing.T, s *Store, f *cdntest.Fixture, a cdn.Attachment, recovery cdn.OperationID) cdn.Plan {
	t.Helper()
	ctx := t.Context()
	edge := f.EdgeA
	if a.Profile == f.EdgeB.Profile() {
		edge = f.EdgeB
	}
	eo, err := edge.Observe(ctx, a)
	if err != nil {
		t.Fatal(err)
	}
	to, err := f.Traffic.Observe(ctx, a)
	if err != nil {
		t.Fatal(err)
	}
	es, err := edge.Actions(a, eo)
	if err != nil {
		t.Fatal(err)
	}
	ts, err := f.Traffic.Actions(a, to)
	if err != nil {
		t.Fatal(err)
	}
	context := cdn.PlanningContext{Attachment: a, PolicyHash: cdn.Hash([]byte("policy")), Initial: []cdn.Observation{eo, to}, RecoveryOf: recovery}
	head, err := s.CDNHead(ctx, a.Owner.App)
	if err == nil {
		context.Previous, context.Retained = head.Attachment.Revision, head.Receipts
	}
	if err != nil && !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
	if recovery != "" {
		context.Retained, err = s.CDNReceipts(ctx, string(recovery))
		if err != nil {
			t.Fatal(err)
		}
	}
	p, err := cdn.NewPlan(context, f.Sample, es, ts)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.SaveCDNPlan(ctx, p); err != nil {
		t.Fatal(err)
	}
	return p
}

func cdnClaim(t *testing.T, s *Store, f *cdntest.Fixture, op string) cdn.Permit {
	t.Helper()
	work, err := s.NextCDN(t.Context(), op)
	if err != nil {
		t.Fatal(err)
	}
	request := work.(cdn.NeedObservation).Request.(cdn.PrepareStep)
	driver := f.EdgeA
	if request.Step.Profile == f.EdgeB.Profile() {
		driver = f.EdgeB
	}
	if request.Step.Profile == f.Traffic.Profile() {
		driver = f.Traffic
	}
	sealed, err := driver.Seal(t.Context(), request.Attachment, request.Step, request.Prior)
	if err != nil {
		t.Fatal(err)
	}
	sealed.OriginHealthy = true
	sealed.OriginEvidence = cdn.Hash([]byte("loopback-origin-qualified"))
	work, err = s.ClaimCDN(t.Context(), op, request.StepIndex, sealed)
	if err != nil {
		t.Fatal(err)
	}
	return work.(cdn.NeedEffect).Permit
}

func cdnSettle(t *testing.T, s *Store, f *cdntest.Fixture, op string, permit cdn.Permit) {
	t.Helper()
	driver := f.EdgeA
	if permit.Action.Profile == f.EdgeB.Profile() {
		driver = f.EdgeB
	}
	if permit.Action.Profile == f.Traffic.Profile() {
		driver = f.Traffic
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		inspection, err := driver.Inspect(t.Context(), permit)
		if err != nil {
			t.Fatal(err)
		}
		work, err := s.RecordCDNInspection(t.Context(), op, inspection)
		if err != nil {
			t.Fatal(err)
		}
		if _, waiting := work.(cdn.Waiting); !waiting {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("conditional loopback effect did not settle")
}

func cdnRunEffects(t *testing.T, s *Store, f *cdntest.Fixture, op string) {
	t.Helper()
	for n := 0; n < cdn.MaxSteps; n++ {
		work, err := s.NextCDN(t.Context(), op)
		if err != nil {
			t.Fatal(err)
		}
		request := work.(cdn.NeedObservation).Request
		if _, done := request.(cdn.NeedQualification); done {
			return
		}
		permit := cdnClaim(t, s, f, op)
		driver := f.EdgeA
		if permit.Action.Profile == f.EdgeB.Profile() {
			driver = f.EdgeB
		}
		if permit.Action.Profile == f.Traffic.Profile() {
			driver = f.Traffic
		}
		result, err := driver.Execute(t.Context(), permit)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = s.RecordCDNResult(t.Context(), op, result); err != nil {
			t.Fatal(err)
		}
		cdnSettle(t, s, f, op, permit)
	}
	t.Fatal("too many CDN effects")
}

func cdnQualify(t *testing.T, s *Store, f *cdntest.Fixture, op string) cdn.Revision {
	t.Helper()
	status, err := s.CDNStatus(t.Context(), op)
	if err != nil {
		t.Fatal(err)
	}
	a := status.Plan.Context.Attachment
	edge := f.EdgeA
	if a.Profile == f.EdgeB.Profile() {
		edge = f.EdgeB
	}
	proof, err := edge.Probe(t.Context(), cdn.Qualification{Attachment: a, Sample: status.Plan.Sample})
	if err != nil {
		t.Fatal(err)
	}
	eo, err := edge.Observe(t.Context(), a)
	if err != nil {
		t.Fatal(err)
	}
	to, err := f.Traffic.Observe(t.Context(), a)
	if err != nil {
		t.Fatal(err)
	}
	readback := []cdn.Observation{eo, to}
	for _, receipt := range finalCDNReceipts(status) {
		driver := f.EdgeA
		if receipt.Profile == f.EdgeB.Profile() {
			driver = f.EdgeB
		}
		if receipt.Profile == f.Traffic.Profile() {
			driver = f.Traffic
		}
		observed, readErr := driver.Observe(t.Context(), receipt.Attachment)
		if readErr != nil {
			t.Fatal(readErr)
		}
		readback = append(readback, observed)
	}
	work, err := s.CommitCDN(t.Context(), op, proof, readback)
	if err != nil {
		t.Fatal(err)
	}
	if finished, ok := work.(cdn.Finished); !ok || !finished.Succeeded {
		t.Fatalf("qualification returned %#v", work)
	}
	head, err := s.CDNHead(t.Context(), a.Owner.App)
	if err != nil {
		t.Fatal(err)
	}
	return head
}

func TestCDNStoreLostClaimAndDelayedReplyNeverGrantsAnotherWriter(t *testing.T) {
	s, f, a := cdnStoreFixture(t)
	p := cdnPlanFixture(t, s, f, a, "")
	op, reused, err := s.AdmitCDN(t.Context(), p.ID, "operator", "first")
	if err != nil || reused {
		t.Fatal(op, reused, err)
	}
	again, reused, err := s.AdmitCDN(t.Context(), p.ID, "operator", "first")
	if err != nil || !reused || again.ID != op.ID {
		t.Fatal("admission replay changed identity", err)
	}
	gate := f.DelayNext(a.Profile.Reference)
	f.DropReply(a.Profile.Reference)
	permit := cdnClaim(t, s, f, op.ID)
	result, err := f.EdgeA.Execute(t.Context(), permit)
	if err != nil {
		t.Fatal(err)
	}
	<-gate.Started
	if _, ok := result.(cdn.Uncertain); !ok {
		t.Fatalf("lost HTTP reply became %#v", result)
	}
	if _, err = s.RecordCDNResult(t.Context(), op.ID, result); err != nil {
		t.Fatal(err)
	}
	inspection, err := f.EdgeA.Inspect(t.Context(), permit)
	if err != nil {
		t.Fatal(err)
	}
	work, err := s.RecordCDNInspection(t.Context(), op.ID, inspection)
	if _, ok := work.(cdn.Waiting); err != nil || !ok {
		t.Fatalf("pending write was settled %#v %v", work, err)
	}
	work, err = s.ClaimCDN(t.Context(), op.ID, 0, cdn.SealedAction{})
	request, ok := work.(cdn.NeedObservation)
	if err != nil || !ok || !reflect.DeepEqual(request.Request.(cdn.InspectIssued).Permit, permit) {
		t.Fatal("lost claim permitted a new writer", err)
	}
	recovery := cdnPlanFixture(t, s, f, a, cdn.OperationID(op.ID))
	if _, _, err = s.AdmitCDN(t.Context(), recovery.ID, "operator", "recover-unknown"); !errors.Is(err, ErrConflict) {
		t.Fatal("unknown native writer reservation transferred", err)
	}
	if _, _, err = s.CreateOperation(t.Context(), ops.Intent{Kind: ops.SecretSet, App: "hello", SecretRef: "test"}, "operator", "same-app"); !errors.Is(err, ErrConflict) {
		t.Fatal("reserved app accepted mutation", err)
	}
	if _, _, err = s.CreateOperation(t.Context(), ops.Intent{Kind: ops.SecretSet, App: "other", SecretRef: "test"}, "operator", "other-app"); err != nil {
		t.Fatal("unrelated app blocked", err)
	}
	close(gate.Release)
	<-gate.Finished
	cdnSettle(t, s, f, op.ID, permit)
	cdnRunEffects(t, s, f, op.ID)
	head := cdnQualify(t, s, f, op.ID)
	if head.Proof.Method != cdn.OperatorHTTP || f.NativeWrites(a.Profile.Reference) != 1 {
		t.Fatal("receipt lost actual proof or repeated native write")
	}
	if _, _, err = s.CreateOperation(t.Context(), ops.Intent{Kind: ops.SecretSet, App: "hello", SecretRef: "test"}, "operator", "released"); err != nil {
		t.Fatal("settled app reservation retained", err)
	}
}

func TestCDNStoreQualificationRejectsWrongHashAndRetainsReservation(t *testing.T) {
	s, f, a := cdnStoreFixture(t)
	p := cdnPlanFixture(t, s, f, a, "")
	op, _, err := s.AdmitCDN(t.Context(), p.ID, "operator", "qualify")
	if err != nil {
		t.Fatal(err)
	}
	cdnRunEffects(t, s, f, op.ID)
	proof, err := f.EdgeA.Probe(t.Context(), cdn.Qualification{Attachment: a, Sample: f.Sample})
	if err != nil {
		t.Fatal(err)
	}
	proof.HashesVerified = false
	if _, err = s.CommitCDN(t.Context(), op.ID, proof, nil); err == nil {
		t.Fatal("invalid proof committed")
	}
	if _, err = s.CDNHead(t.Context(), "hello"); !errors.Is(err, ErrNotFound) {
		t.Fatal("failed qualification advanced revision")
	}
	if _, _, err = s.CreateOperation(t.Context(), ops.Intent{Kind: ops.SecretSet, App: "hello", SecretRef: "test"}, "operator", "blocked"); !errors.Is(err, ErrConflict) {
		t.Fatal("failed qualification released app", err)
	}
}

func TestCDNStoreNoopRequiresNewProofAndExactFreshReadback(t *testing.T) {
	s, f, a := cdnStoreFixture(t)
	p := cdnPlanFixture(t, s, f, a, "")
	op, _, err := s.AdmitCDN(t.Context(), p.ID, "operator", "enable")
	if err != nil {
		t.Fatal(err)
	}
	cdnRunEffects(t, s, f, op.ID)
	head := cdnQualify(t, s, f, op.ID)
	noop := cdnPlanFixture(t, s, f, head.Attachment, "")
	if len(noop.Steps) != 0 {
		t.Fatal("already desired operation repeated writes")
	}
	next, _, err := s.AdmitCDN(t.Context(), noop.ID, "operator", "noop")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.CommitCDN(t.Context(), next.ID, head.Proof, head.Readback); err == nil {
		t.Fatal("old qualification was relabeled fresh")
	}
	fresh := cdnQualify(t, s, f, next.ID)
	if fresh.Operation != head.Operation || f.NativeWrites(a.Profile.Reference) != 1 {
		t.Fatal("noop changed immutable history or wrote again")
	}
}

func TestCDNStoreKnownSettledCutoverCanTransferToFreshRestorePlan(t *testing.T) {
	s, f, a := cdnStoreFixture(t)
	p := cdnPlanFixture(t, s, f, a, "")
	op, _, err := s.AdmitCDN(t.Context(), p.ID, "operator", "failed-qualified")
	if err != nil {
		t.Fatal(err)
	}
	cdnRunEffects(t, s, f, op.ID)
	a.Mode = cdn.Direct
	a.Revision = cdn.RevisionFor(a, cdn.Hash([]byte("policy")), "")
	restore := cdnPlanFixture(t, s, f, a, cdn.OperationID(op.ID))
	recovery, _, err := s.AdmitCDN(t.Context(), restore.ID, "operator", "restore")
	if err != nil {
		t.Fatal(err)
	}
	source, err := s.GetOperation(t.Context(), op.ID)
	if err != nil || source.State != ops.Failed {
		t.Fatal("known settled source not retained as failed", err)
	}
	cdnRunEffects(t, s, f, recovery.ID)
	head := cdnQualify(t, s, f, recovery.ID)
	if head.Attachment.Mode != cdn.Direct {
		t.Fatal("fresh direct-origin proof did not commit restore")
	}
}

func TestCDNStoreSwitchRetainsOriginalProviderReadbackAndAppRelease(t *testing.T) {
	s, f, a := cdnStoreFixture(t)
	p := cdnPlanFixture(t, s, f, a, "")
	op, _, err := s.AdmitCDN(t.Context(), p.ID, "operator", "edge-a")
	if err != nil {
		t.Fatal(err)
	}
	cdnRunEffects(t, s, f, op.ID)
	head := cdnQualify(t, s, f, op.ID)
	a.Profile = f.EdgeB.Profile()
	a.Revision = cdn.RevisionFor(a, cdn.Hash([]byte("policy")), head.Attachment.Revision)
	switchPlan := cdnPlanFixture(t, s, f, a, "")
	next, _, err := s.AdmitCDN(t.Context(), switchPlan.ID, "operator", "edge-b")
	if err != nil {
		t.Fatal(err)
	}
	cdnRunEffects(t, s, f, next.ID)
	nextHead := cdnQualify(t, s, f, next.ID)
	oldPreserved := false
	for _, receipt := range nextHead.Receipts {
		if receipt.Profile == f.EdgeA.Profile() && receipt.Attachment.Revision == head.Attachment.Revision {
			oldPreserved = true
		}
	}
	if !oldPreserved || nextHead.Attachment.Profile != f.EdgeB.Profile() {
		t.Fatal("switch lost original edge ownership")
	}
	release, err := s.CurrentRelease(t.Context(), "hello")
	if err != nil || release.ID != a.SourceRelease {
		t.Fatal("CDN switch changed application release", err)
	}
}

func TestCDNStoreNativeVersionDriftCannotBeAdoptedByNoop(t *testing.T) {
	s, f, a := cdnStoreFixture(t)
	p := cdnPlanFixture(t, s, f, a, "")
	op, _, err := s.AdmitCDN(t.Context(), p.ID, "operator", "first")
	if err != nil {
		t.Fatal(err)
	}
	cdnRunEffects(t, s, f, op.ID)
	head := cdnQualify(t, s, f, op.ID)
	noop := cdnPlanFixture(t, s, f, head.Attachment, "")
	for index := range noop.Context.Initial {
		if noop.Context.Initial[index].Profile == a.Profile {
			noop.Context.Initial[index].Resources[0].Version = "foreign-v2"
		}
	}
	noop, err = cdn.NewPlan(noop.Context, noop.Sample, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.SaveCDNPlan(t.Context(), noop); err != nil {
		t.Fatal(err)
	}
	if _, _, err = s.AdmitCDN(t.Context(), noop.ID, "operator", "drift"); !errors.Is(err, ErrConflict) {
		t.Fatal("same configuration hash hid native version drift", err)
	}
}

func cdnRemovePlanFixture(t *testing.T, s *Store) plan.Plan {
	t.Helper()
	r, err := s.CurrentRelease(t.Context(), "hello")
	if err != nil {
		t.Fatal(err)
	}
	_, d, err := s.LoadPlan(t.Context(), r.PlanID)
	if err != nil {
		t.Fatal(err)
	}
	in := fixture(t)
	in.Desired = d
	in.Snapshot.Generation = target.Known(uint64(1))
	in.State, err = s.LoadBrineState(t.Context(), in.Snapshot.Identity, 1)
	if err != nil {
		t.Fatal(err)
	}
	in.Snapshot.Apps = target.Known([]target.App{{Name: "hello", Image: target.Known(target.Image{Digest: r.Image.Digest, Platform: r.Image.Platform}), AllocatedHostPort: target.Known(r.HostPort), QuadletUnits: target.Known(r.Units), Secrets: target.Known([]target.Secret{})}})
	in.Snapshot.CaddyConfig = target.Known(target.CaddyConfigSet{Generation: r.CaddyGeneration, Files: []target.CaddyFile{r.CaddyFile}})
	p, err := plan.BuildRemove(in)
	if err != nil || p.Kind != plan.Update {
		t.Fatal(p, err)
	}
	if _, err = s.SavePlan(t.Context(), p, d); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestCDNStoreRemovalRequiresDirectAndRetiresAttachmentIncarnation(t *testing.T) {
	s, f, a := cdnStoreFixture(t)
	p := cdnPlanFixture(t, s, f, a, "")
	op, _, err := s.AdmitCDN(t.Context(), p.ID, "operator", "enable")
	if err != nil {
		t.Fatal(err)
	}
	cdnRunEffects(t, s, f, op.ID)
	head := cdnQualify(t, s, f, op.ID)
	removal := cdnRemovePlanFixture(t, s)
	if _, _, err = s.CreateOperation(t.Context(), ops.Intent{Kind: ops.Deploy, PlanID: removal.Hash}, "operator", "remove-enabled"); !errors.Is(err, ErrConflict) {
		t.Fatal("enabled CDN app admitted removal", err)
	}
	a.Mode = cdn.Direct
	a.Revision = cdn.RevisionFor(a, cdn.Hash([]byte("policy")), head.Attachment.Revision)
	disable := cdnPlanFixture(t, s, f, a, "")
	direct, _, err := s.AdmitCDN(t.Context(), disable.ID, "operator", "direct")
	if err != nil {
		t.Fatal(err)
	}
	cdnRunEffects(t, s, f, direct.ID)
	cdnQualify(t, s, f, direct.ID)
	remove, _, err := s.CreateOperation(t.Context(), ops.Intent{Kind: ops.Deploy, PlanID: removal.Hash}, "operator", "remove-disabled")
	if err != nil {
		t.Fatal(err)
	}
	if err = s.RetireApp(t.Context(), remove.ID, "hello", a.SourceRelease); err != nil {
		t.Fatal(err)
	}
	if _, err = s.CDNHead(t.Context(), "hello"); !errors.Is(err, ErrNotFound) {
		t.Fatal("retired app retained active CDN incarnation", err)
	}
	if _, err = s.CDNRevision(t.Context(), "hello", a.Revision); err != nil {
		t.Fatal("retired CDN history disappeared", err)
	}
}
