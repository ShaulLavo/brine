package host

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"testing"

	"github.com/ShaulLavo/brine/internal/cdn"
	"github.com/ShaulLavo/brine/internal/cdn/cdntest"
	"github.com/ShaulLavo/brine/internal/cdnoperator"
	"github.com/ShaulLavo/brine/internal/dispatch"
	"github.com/ShaulLavo/brine/internal/jobs"
	"github.com/ShaulLavo/brine/internal/policy"
	"github.com/ShaulLavo/brine/internal/result"
	"github.com/pelletier/go-toml/v2"
)

type fixtureCDNDrivers struct{ fixture *cdntest.Fixture }

func (d fixtureCDNDrivers) Edge(scope cdn.ProfileScope) (cdn.EdgeDriver, error) {
	if scope == d.fixture.EdgeA.Profile() {
		return d.fixture.EdgeA, nil
	}
	if scope == d.fixture.EdgeB.Profile() {
		return d.fixture.EdgeB, nil
	}
	return nil, cdn.ErrInvalid
}
func (d fixtureCDNDrivers) Traffic(scope cdn.ProfileScope) (cdn.TrafficDriver, error) {
	if scope == d.fixture.Traffic.Profile() {
		return d.fixture.Traffic, nil
	}
	return nil, cdn.ErrInvalid
}

func cdnHostFixture(t *testing.T) (*deployRig, *cdntest.Fixture, cdnoperator.Coordinator, cdnoperator.PlanRequest) {
	t.Helper()
	rig := newDeployRig(t)
	planned := rig.call(t, "plan", dispatch.PlanArgs{Spec: rig.spec}).Data.(dispatch.Planned)
	accepted := rig.call(t, "apply", dispatch.ApplyArgs{PlanID: planned.PlanID, IdempotencyKey: "cdn-origin"}).Data.(jobs.Accepted)
	if err := rig.runner.Run(t.Context(), accepted.OperationID); err != nil {
		t.Fatal(err)
	}
	fixture := cdntest.New(t)
	binding := fixture.Attachment(fixture.EdgeA.Profile().Reference).Binding
	identity, _ := json.Marshal(rig.inventory.snapshot.Identity)
	binding.Target, binding.App, binding.Hostname = cdn.Hash(identity), "hello", "hello.example.com"
	binding.Origin.Host, binding.TrafficAuthority.Hostname = binding.Hostname, binding.Hostname
	raw, err := os.ReadFile("../policy/testdata/operator.toml")
	if err != nil {
		t.Fatal(err)
	}
	raw = bytes.ReplaceAll(raw, []byte("Registry.Example.com:5000"), []byte("ghcr.io"))
	var document map[string]any
	if err := toml.Unmarshal(raw, &document); err != nil {
		t.Fatal(err)
	}
	document["cdn_bindings"] = []cdn.Binding{binding}
	raw, err = toml.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	rig.policy.p, err = policy.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	rig.server.CDN = cdnOperations{service: rig.service}
	host := wireCDNHost{server: rig.server}
	coordinator := cdnoperator.Coordinator{Host: host, Drivers: fixtureCDNDrivers{fixture}, OriginCheck: func(ctx context.Context, q cdn.Qualification) (cdn.Fingerprint, error) {
		return cdnoperator.VerifyOriginWithClient(ctx, q, fixture.Client)
	}}
	request := cdnoperator.PlanRequest{App: "hello", Binding: "public", Profile: fixture.EdgeA.Profile().Reference, Mode: cdn.Enabled, SamplePath: fixture.Sample.Path}
	return rig, fixture, coordinator, request
}

func TestCDNOperatorHostWireServesSwitchDisableAndNoOp(t *testing.T) {
	rig, fixture, coordinator, request := cdnHostFixture(t)
	ctx := t.Context()
	source, err := rig.store.CurrentRelease(ctx, "hello")
	if err != nil {
		t.Fatal(err)
	}
	p, err := coordinator.Plan(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	progress, err := coordinator.Apply(ctx, cdnoperator.ApplyRequest{PlanID: p.ID, IdempotencyKey: "cdn-enable"})
	assertCDNFinished(t, progress, err)
	writes := fixture.NativeWrites(request.Profile)
	again, err := coordinator.Apply(ctx, cdnoperator.ApplyRequest{PlanID: p.ID, IdempotencyKey: "cdn-enable"})
	assertCDNFinished(t, again, err)
	if again.Operation != progress.Operation || fixture.NativeWrites(request.Profile) != writes {
		t.Fatal("replayed admission wrote native state")
	}
	fixture.Evict(request.Profile)
	status, err := coordinator.Status(ctx, cdnoperator.StatusRequest{App: "hello"}, true)
	if err != nil || status.Proof == nil || !status.Proof.LaterHit {
		t.Fatalf("fresh full GET proof missing: %v", err)
	}
	request.Profile = fixture.EdgeB.Profile().Reference
	p, err = coordinator.Plan(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	progress, err = coordinator.Apply(ctx, cdnoperator.ApplyRequest{PlanID: p.ID, IdempotencyKey: "cdn-switch"})
	assertCDNFinished(t, progress, err)
	request.Mode = cdn.Direct
	p, err = coordinator.Plan(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	progress, err = coordinator.Apply(ctx, cdnoperator.ApplyRequest{PlanID: p.ID, IdempotencyKey: "cdn-disable"})
	assertCDNFinished(t, progress, err)
	directRevision := p.Context.Attachment.Revision
	p, err = coordinator.Plan(ctx, request)
	if err != nil || len(p.Steps) != 0 || p.Context.Attachment.Revision != directRevision {
		t.Fatalf("same direct origin was not a no-op: %v", err)
	}
	progress, err = coordinator.Apply(ctx, cdnoperator.ApplyRequest{PlanID: p.ID, IdempotencyKey: "cdn-noop"})
	assertCDNFinished(t, progress, err)
	current, err := rig.store.CurrentRelease(ctx, "hello")
	if err != nil || current.ID != source.ID {
		t.Fatal("CDN changed app release", err)
	}
}

func assertCDNFinished(t *testing.T, progress cdnoperator.Progress, err error) {
	t.Helper()
	finished, ok := progress.Work.(cdn.Finished)
	if err != nil || !ok || !finished.Succeeded {
		t.Fatalf("CDN operation did not commit: %T %v", progress.Work, err)
	}
}

type lostSealHost struct {
	cdnoperator.Host
	dropped bool
}

func (h *lostSealHost) Seal(ctx context.Context, request cdnoperator.SealRequest) (cdnoperator.Progress, error) {
	progress, err := h.Host.Seal(ctx, request)
	if err == nil && !h.dropped {
		h.dropped = true
		return cdnoperator.Progress{}, errors.New("synthetic lost permit response")
	}
	return progress, err
}

func TestCDNOperatorLostClaimNeverExecutesOnResume(t *testing.T) {
	_, fixture, coordinator, request := cdnHostFixture(t)
	host := &lostSealHost{Host: coordinator.Host}
	coordinator.Host = host
	p, err := coordinator.Plan(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	progress, err := coordinator.Apply(t.Context(), cdnoperator.ApplyRequest{PlanID: p.ID, IdempotencyKey: "lost-claim"})
	if err == nil {
		t.Fatal("lost claim was hidden")
	}
	progress, err = coordinator.Resume(t.Context(), progress.Operation)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := progress.Work.(cdn.Waiting); !ok || fixture.NativeWrites(request.Profile) != 0 {
		t.Fatal("resume reissued a lost claim")
	}
}

type lostCDNReplyHost struct {
	cdnoperator.Host
	admission bool
	result    bool
}

func (h *lostCDNReplyHost) Apply(ctx context.Context, request cdnoperator.ApplyRequest) (cdnoperator.Progress, error) {
	progress, err := h.Host.Apply(ctx, request)
	if err == nil && h.admission {
		h.admission = false
		return cdnoperator.Progress{}, errors.New("synthetic lost admission reply")
	}
	return progress, err
}

func (h *lostCDNReplyHost) RecordResult(ctx context.Context, request cdnoperator.ResultRequest) (cdnoperator.Progress, error) {
	progress, err := h.Host.RecordResult(ctx, request)
	if err == nil && h.result {
		h.result = false
		return cdnoperator.Progress{}, errors.New("synthetic lost result reply")
	}
	return progress, err
}

func TestCDNOperatorLostAdmissionAndResultRepliesResumeHostJournal(t *testing.T) {
	_, fixture, coordinator, request := cdnHostFixture(t)
	host := &lostCDNReplyHost{Host: coordinator.Host, admission: true, result: true}
	coordinator.Host = host
	p, err := coordinator.Plan(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	input := cdnoperator.ApplyRequest{PlanID: p.ID, IdempotencyKey: "lost-replies"}
	if _, err := coordinator.Apply(t.Context(), input); err == nil || fixture.NativeWrites(request.Profile) != 0 {
		t.Fatal("lost admission issued native writes", err)
	}
	progress, err := coordinator.Apply(t.Context(), input)
	if err == nil || progress.Operation == "" || fixture.NativeWrites(request.Profile) != 1 {
		t.Fatal("lost result failed to preserve resumable operation", err)
	}
	progress, err = coordinator.Resume(t.Context(), progress.Operation)
	assertCDNFinished(t, progress, err)
	if fixture.NativeWrites(request.Profile) != 1 {
		t.Fatal("resume repeated the acknowledged native effect")
	}
}

func TestCDNHostRejectsStalePolicyAndForeignRequester(t *testing.T) {
	rig, fixture, coordinator, request := cdnHostFixture(t)
	p, err := coordinator.Plan(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	before := rig.policy.p
	rig.policy.p, _, err = before.EditPermissions([]policy.PermissionAction{{Kind: policy.AllowDomain, Domain: "another.example"}})
	if err != nil {
		t.Fatal(err)
	}
	input := cdnoperator.ApplyRequest{PlanID: p.ID, IdempotencyKey: "stale-policy"}
	if _, err := coordinator.Apply(t.Context(), input); err == nil || fixture.NativeWrites(request.Profile) != 0 {
		t.Fatal("stale policy admitted native work", err)
	}
	rig.policy.p = before
	progress, err := coordinator.Apply(t.Context(), input)
	assertCDNFinished(t, progress, err)
	foreign := rig.service
	foreign.Requester = "another-operator"
	if _, err := (cdnOperations{service: foreign}).Next(t.Context(), cdnoperator.OperationRequest{Operation: progress.Operation}); err == nil {
		t.Fatal("another requester read operation work")
	}
	request.App = "other"
	if _, err := coordinator.Plan(t.Context(), request); err == nil {
		t.Fatal("another app borrowed protected CDN binding")
	}
}

func TestCDNHostConflictingAdmissionReturnsConflictEnvelope(t *testing.T) {
	_, _, coordinator, request := cdnHostFixture(t)
	p, err := coordinator.Plan(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	input := cdnoperator.ApplyRequest{PlanID: p.ID, IdempotencyKey: "duplicate-key"}
	progress, err := coordinator.Apply(t.Context(), input)
	assertCDNFinished(t, progress, err)
	request.Mode = cdn.Direct
	p, err = coordinator.Plan(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	input.PlanID = p.ID
	if _, err := coordinator.Apply(t.Context(), input); err == nil || result.Classify(err).Code() != result.Conflict {
		t.Fatal("conflicting admission lost its public error category", err)
	}
}

type wireCDNHost struct{ server *dispatch.Server }

func cdnWireCall[T any](ctx context.Context, host wireCDNHost, op string, args any) (T, error) {
	var zero T
	raw, err := json.Marshal(args)
	if err != nil {
		return zero, err
	}
	request, err := dispatch.EncodeRequest(dispatch.Request{SchemaVersion: 1, Op: op, RequestID: "cdn-fixture", Args: raw})
	if err != nil {
		return zero, err
	}
	envelope, err := host.server.Handle(ctx, bytes.NewReader(request))
	if err != nil {
		return zero, err
	}
	raw, err = json.Marshal(envelope)
	if err != nil {
		return zero, err
	}
	envelope, err = dispatch.DecodeResponse(raw, op)
	if err != nil {
		return zero, err
	}
	value, ok := envelope.Data.(T)
	if !ok {
		return zero, cdn.ErrInvalid
	}
	return value, nil
}

func (h wireCDNHost) Context(ctx context.Context, r cdnoperator.PlanRequest) (cdnoperator.Planning, error) {
	return cdnWireCall[cdnoperator.Planning](ctx, h, "cdn_context", r)
}
func (h wireCDNHost) SavePlan(ctx context.Context, p cdn.Plan) (cdn.Plan, error) {
	return cdnWireCall[cdn.Plan](ctx, h, "cdn_plan", dispatch.CDNSavePlanArgs{Plan: p})
}
func (h wireCDNHost) Apply(ctx context.Context, r cdnoperator.ApplyRequest) (cdnoperator.Progress, error) {
	return cdnWireCall[cdnoperator.Progress](ctx, h, "cdn_apply", r)
}
func (h wireCDNHost) Next(ctx context.Context, r cdnoperator.OperationRequest) (cdnoperator.Progress, error) {
	return cdnWireCall[cdnoperator.Progress](ctx, h, "cdn_next", r)
}
func (h wireCDNHost) Seal(ctx context.Context, r cdnoperator.SealRequest) (cdnoperator.Progress, error) {
	return cdnWireCall[cdnoperator.Progress](ctx, h, "cdn_seal", r)
}
func (h wireCDNHost) RecordResult(ctx context.Context, r cdnoperator.ResultRequest) (cdnoperator.Progress, error) {
	return cdnWireCall[cdnoperator.Progress](ctx, h, "cdn_result", r)
}
func (h wireCDNHost) RecordInspection(ctx context.Context, r cdnoperator.InspectionRequest) (cdnoperator.Progress, error) {
	return cdnWireCall[cdnoperator.Progress](ctx, h, "cdn_inspect", r)
}
func (h wireCDNHost) Qualify(ctx context.Context, r cdnoperator.ProofRequest) (cdnoperator.Progress, error) {
	return cdnWireCall[cdnoperator.Progress](ctx, h, "cdn_proof", r)
}
func (h wireCDNHost) Status(ctx context.Context, r cdnoperator.StatusRequest) (cdnoperator.Status, error) {
	return cdnWireCall[cdnoperator.Status](ctx, h, "cdn_status", r)
}
