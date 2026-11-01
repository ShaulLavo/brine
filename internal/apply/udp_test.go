package apply

import (
	"context"
	"errors"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/ShaulLavo/brine/internal/ops"
	"github.com/ShaulLavo/brine/internal/plan"
	"github.com/ShaulLavo/brine/internal/policy"
	"github.com/ShaulLavo/brine/internal/spec"
	"github.com/ShaulLavo/brine/internal/target"
)

type udpRig struct {
	*rig
	reservation string
	failProof   string
	prepared    int
}

func newUDPRig(t testing.TB, mode string) *udpRig {
	t.Helper()
	r := newRig(t, mode != "create")
	d := &policy.UDPDesired{ContainerPort: 4433, Exposure: spec.UDPPublic, Grant: policy.UDPGrant{App: "hello", Exposure: spec.UDPPublic, BindAddress: "192.0.2.10", ClientAddress: "relay.example.test", HostPorts: policy.PortRange{Min: 4433, Max: 4435}}}
	in := r.facts.Input
	in.Desired.UDP = d
	in.Snapshot.LocalAddresses = target.Known([]string{"192.0.2.10"})
	in.State.UDP = &plan.UDPState{Incarnations: []plan.AppIncarnation{}, Reservations: []plan.UDPReservation{}}
	if mode != "create" {
		incarnation := "sha256:" + strings.Repeat("e", 64)
		endpoint := plan.UDPEndpoint{Family: "ipv4", BindAddress: "192.0.2.10", HostPort: 4433}
		allocation := &plan.UDPAllocation{Endpoint: endpoint, ContainerPort: 4433, ClientAddress: d.Grant.ClientAddress, Exposure: d.Exposure, GrantHash: d.Grant.Hash()}
		in.State.UDP.Incarnations = []plan.AppIncarnation{{App: "hello", ID: incarnation}}
		in.State.UDP.Reservations = []plan.UDPReservation{{ID: "udp-old", App: "hello", Incarnation: incarnation, State: "active", Endpoint: endpoint}}
		in.State.Releases[0].UDP = allocation
		r.oldDesired.UDP = d
		in.State.Releases[0].Desired = r.oldDesired
		(*in.Snapshot.Apps.Value)[0].UDPPublication = target.Known(target.UDPPublication{BindAddress: endpoint.BindAddress, HostPort: 4433, ContainerPort: 4433, NetworkMode: "pasta"})
		old := in
		old.Desired = r.oldDesired
		var err error
		r.oldPlan, err = plan.Build(old)
		if err != nil || r.oldPlan.Kind == plan.Conflict {
			t.Fatal(r.oldPlan.Conflicts, err)
		}
		r.release.PlanID = r.oldPlan.Hash
		if mode == "move" {
			next := *d
			next.Grant.HostPorts = policy.PortRange{Min: 4434, Max: 4435}
			in.Desired.UDP = &next
		}
		if mode == "release" {
			in.Desired.UDP = nil
		}
	}
	var err error
	r.plan, err = plan.Build(in)
	if err != nil || r.plan.Kind == plan.Conflict {
		t.Fatal(r.plan.Conflicts, err)
	}
	r.desired = in.Desired
	r.facts.Input = in
	u := &udpRig{rig: r, reservation: "unprepared"}
	r.executor.UDPReservations = u
	r.executor.UDPChecks = u
	r.executor.Releases = u
	return u
}
func (u *udpRig) PrepareUDP(_ context.Context, _ string, _ plan.Plan) error {
	err := u.hit("prepare_udp")
	if err == nil || u.unknownStep == "prepare_udp" {
		u.reservation = "prepared"
		u.prepared++
	}
	return err
}
func (u *udpRig) UDPPreparation(context.Context, string, plan.Plan) (string, error) {
	return u.reservation, nil
}
func (u *udpRig) RevertUDP(_ context.Context, _ string, _ plan.Plan) error {
	name := "revert_udp"
	if u.intent == "rollback_udp" {
		name = u.intent
	}
	err := u.hit(name)
	if err == nil || u.unknownStep == name {
		u.reservation = "reverted"
	}
	return err
}
func (u *udpRig) Ready(_ context.Context, _ string, _ plan.Plan, previous bool) error {
	name := "check_udp"
	if previous {
		name = "previous_udp"
	}
	if u.failProof == name {
		return injected
	}
	if u.intent == "rollback_udp_check" {
		u.effects = append(u.effects, name)
		return u.hit(u.intent)
	}
	return u.hit(name)
}
func (u *udpRig) Absent(_ context.Context, _ string, p plan.Plan, e plan.UDPEndpoint) error {
	name := "check_udp_retired"
	if p.UDP.Next != nil && e == p.UDP.Next.Endpoint {
		name = "candidate_udp_absent"
	}
	if u.failProof == name {
		return injected
	}
	if u.intent == "rollback_udp_check" {
		u.effects = append(u.effects, name)
		return u.hit(u.intent)
	}
	return u.hit(name)
}
func (u *udpRig) CommitRelease(ctx context.Context, app string, r Release) error {
	err := u.rig.CommitRelease(ctx, app, r)
	if u.committed {
		u.reservation = "committed"
	}
	return err
}
func (u *udpRig) RetireApp(ctx context.Context, id, app, release string) error {
	err := u.rig.RetireApp(ctx, id, app, release)
	if u.committed {
		u.reservation = "committed"
	}
	return err
}
func TestUDPApplyTransitionProofAndSettlementOrder(t *testing.T) {
	for _, mode := range []string{"create", "retain", "move", "release"} {
		t.Run(mode, func(t *testing.T) {
			u := newUDPRig(t, mode)
			if err := u.executor.Run(t.Context(), "operation", u.plan, u.desired); err != nil {
				t.Fatal(err)
			}
			if u.state != Succeeded || udpPreparationNeeded(u.plan) && u.reservation != "committed" {
				t.Fatal(u.state, u.reservation)
			}
			effects := u.effects
			if mode != "retain" && slices.Index(effects, "prepare_udp") > slices.Index(effects, "stage_unit") {
				t.Fatal(effects)
			}
			if mode != "release" && slices.Index(effects, "check_udp") > slices.Index(effects, "commit") {
				t.Fatal(effects)
			}
			if mode == "move" || mode == "release" {
				if slices.Index(effects, "check_udp_retired") < 0 || slices.Index(effects, "check_udp_retired") > slices.Index(effects, "commit") {
					t.Fatal(effects)
				}
			}
		})
	}
}
func TestUDPEarlyFailureRevertsWithoutRuntimeProof(t *testing.T) {
	for _, step := range []string{"prepare_udp", "stage_unit"} {
		u := newUDPRig(t, "create")
		u.failStep = step
		if err := u.executor.Run(t.Context(), "operation", u.plan, u.desired); err == nil {
			t.Fatal("failure ignored")
		}
		if u.state != Failed || u.reservation == "prepared" {
			t.Fatal(step, u.state, u.reservation)
		}
		if slices.Contains(u.effects, "candidate_udp_absent") {
			t.Fatal("runtime proof before runtime touched")
		}
	}
}
func TestUDPCompensationRequiresPreviousReadyAndCandidateAbsent(t *testing.T) {
	for _, mode := range []string{"create", "retain", "move", "release"} {
		for _, proof := range []string{"", "previous_udp", "candidate_udp_absent"} {
			t.Run(mode+"/"+proof, func(t *testing.T) {
				u := newUDPRig(t, mode)
				u.failStep = "check_direct"
				u.failProof = proof
				err := u.executor.Run(t.Context(), "operation", u.plan, u.desired)
				if err == nil {
					t.Fatal("failure ignored")
				}
				needed := proof == "previous_udp" && mode != "create" || proof == "candidate_udp_absent" && (mode == "create" || mode == "move")
				want := RolledBack
				if needed {
					want = RecoveryRequired
				}
				if u.state != want {
					t.Fatalf("state %s want %s, effects %v", u.state, want, u.effects)
				}
				if needed && udpPreparationNeeded(u.plan) && u.reservation != "prepared" {
					t.Fatal("failed proof settled claim", u.reservation)
				}
				if !needed && udpPreparationNeeded(u.plan) && u.reservation != "reverted" {
					t.Fatal("compensation leaked claim", u.reservation)
				}
			})
		}
	}
}
func TestUDPRetiredRemnantNeverCommitsHealthyCandidate(t *testing.T) {
	u := newUDPRig(t, "move")
	u.failProof = "check_udp_retired"
	if err := u.executor.Run(t.Context(), "operation", u.plan, u.desired); err == nil {
		t.Fatal("remnant ignored")
	}
	if u.state != RolledBack || u.committed || u.reservation != "reverted" {
		t.Fatal(u.state, u.committed, u.reservation)
	}
}
func TestUDPUnknownPreparationAndRevertReadBack(t *testing.T) {
	for _, step := range []string{"prepare_udp", "revert_udp", "rollback_udp"} {
		u := newUDPRig(t, "create")
		u.unknownStep = step
		if step == "revert_udp" {
			u.failStep = "stage_unit"
		}
		if step == "rollback_udp" {
			u.failStep = "check_direct"
		}
		err := u.executor.Run(t.Context(), "operation", u.plan, u.desired)
		if step == "prepare_udp" && (err != nil || u.state != Succeeded || u.prepared != 1) {
			t.Fatal(u.state, err)
		}
		if step != "prepare_udp" && (err == nil || u.state == RecoveryRequired || u.reservation != "reverted") {
			t.Fatal(u.state, u.reservation, err)
		}
	}
}
func TestUDPResumePreparedClaimRunsNoSecondPreparation(t *testing.T) {
	u := newUDPRig(t, "create")
	u.reservation = "prepared"
	claim := plan.UDPReservation{ID: "udp-operation", App: u.plan.App, Incarnation: u.plan.ResolvedIncarnation(), State: "staged", Owner: "operation", Endpoint: u.plan.UDP.Next.Endpoint}
	u.facts.Input.State.UDP.Reservations = []plan.UDPReservation{claim}
	u.executor.Facts = FactsFunc(func(context.Context) (Facts, error) { return u.facts, nil })
	events := recoveryEvents("preflight", "pull_image", "verify_image", "ensure_secrets", "prepare_udp")
	u.state = Preparing
	assessment, err := u.executor.InspectRecovery(t.Context(), Operation{ID: "operation", PlanID: u.plan.Hash, State: Preparing}, u.plan, u.desired, events)
	if err != nil || assessment.Action != ResumeForward {
		t.Fatal(assessment.Action, err)
	}
	if err := u.executor.Recover(t.Context(), assessment); err != nil {
		t.Fatal(err)
	}
	if u.prepared != 0 || u.state != Succeeded {
		t.Fatal(u.prepared, u.state)
	}
}
func TestUDPLifecycleKeepsReservationAndChecksStarts(t *testing.T) {
	for _, action := range []plan.ChangeKind{plan.StopApp, plan.StartApp, plan.RestartApp} {
		u := newUDPRig(t, "retain")
		in := u.facts.Input
		in.Desired = u.oldDesired
		p, err := plan.BuildLifecycle(in, action)
		if err != nil {
			t.Fatal(err)
		}
		u.plan, u.desired, u.facts.Input = p, in.Desired, in
		if err := u.executor.Run(t.Context(), "operation", p, in.Desired); err != nil {
			t.Fatal(action, err)
		}
		if u.prepared != 0 || u.reservation != "unprepared" {
			t.Fatal("lifecycle changed reservation")
		}
		if slices.Contains(u.effects, "check_udp") != (action != plan.StopApp) {
			t.Fatal(action, u.effects)
		}
	}
}
func TestUDPPreparedDriftChoosesSafeReservationRevert(t *testing.T) {
	u := newUDPRig(t, "create")
	u.reservation = "prepared"
	u.state = Preparing
	u.facts.Input.State.UDP.Reservations = []plan.UDPReservation{{ID: "udp-operation", App: u.plan.App, Incarnation: u.plan.ResolvedIncarnation(), State: "staged", Owner: "operation", Endpoint: u.plan.UDP.Next.Endpoint}}
	u.facts.Input.Snapshot.UDPSockets = target.Known([]target.UDPSocket{{Address: u.plan.UDP.Next.Endpoint.BindAddress, Port: u.plan.UDP.Next.Endpoint.HostPort}})
	u.executor.Facts = FactsFunc(func(context.Context) (Facts, error) { return u.facts, nil })
	events := recoveryEvents("preflight", "pull_image", "verify_image", "ensure_secrets", "prepare_udp")
	events[len(events)-1].Payload = []byte(`{"step":"prepare_udp","outcome":"completed"}`)
	assessment, err := u.executor.InspectRecovery(t.Context(), Operation{ID: "operation", PlanID: u.plan.Hash, State: Preparing}, u.plan, u.desired, events)
	if err != nil || assessment.Action != RestorePrevious {
		t.Fatal(assessment.Action, err)
	}
	if err := u.executor.Recover(t.Context(), assessment); err == nil {
		t.Fatal("drift not reported")
	}
	if u.state != Failed || u.reservation != "reverted" {
		t.Fatal(u.state, u.reservation)
	}
}
func TestUDPJournalFailureNeverReleasesUncertainRuntime(t *testing.T) {
	u := newUDPRig(t, "move")
	u.failOutcome = "check_udp"
	err := u.executor.Run(t.Context(), "operation", u.plan, u.desired)
	var failure *Error
	if !errors.As(err, &failure) || u.state != RecoveryRequired || u.reservation != "prepared" {
		t.Fatal(u.state, u.reservation, err)
	}
	if reflect.DeepEqual(u.effects, []string{}) {
		t.Fatal("no effects")
	}
}

func TestUDPLifecycleRecoveryRechecksWithoutRepeatingSettledEffects(t *testing.T) {
	for _, action := range []plan.ChangeKind{plan.StartApp, plan.StopApp, plan.RestartApp} {
		u := newUDPRig(t, "retain")
		in := u.facts.Input
		in.Desired = u.oldDesired
		p, err := plan.BuildLifecycle(in, action)
		if err != nil {
			t.Fatal(err)
		}
		u.plan, u.desired, u.facts.Input = p, in.Desired, in
		u.executor.Facts = FactsFunc(func(context.Context) (Facts, error) { return u.facts, nil })
		u.state = Preflight
		if action == plan.StartApp {
			u.active = false
		}
		assessment, err := u.executor.InspectRecovery(t.Context(), Operation{ID: "operation", PlanID: p.Hash, State: Preflight}, p, u.desired, recoveryEvents("preflight"))
		if err != nil || assessment.Action != ResumeForward {
			t.Fatal(action, assessment.Action, err)
		}
		if err := u.executor.Recover(t.Context(), assessment); err != nil {
			t.Fatal(action, err)
		}
		if u.state != Succeeded || u.prepared != 0 {
			t.Fatal(action, u.state, u.prepared)
		}
	}
}
func TestUDPStoppedLifecycleResolutionUsesItsSourcePrefix(t *testing.T) {
	u := newUDPRig(t, "retain")
	in := u.facts.Input
	in.Desired = u.oldDesired
	p, err := plan.BuildLifecycle(in, plan.StopApp)
	if err != nil {
		t.Fatal(err)
	}
	u.plan, u.desired, u.facts.Input = p, in.Desired, in
	u.executor.Facts = FactsFunc(func(context.Context) (Facts, error) { return u.facts, nil })
	u.active = false
	u.state = Queued
	u.operationKind = ops.Resolve
	source := Operation{ID: "source", Kind: ops.Deploy, PlanID: p.Hash, State: RecoveryRequired}
	successor := Operation{ID: "operation", Kind: ops.Resolve, RecoveryOf: source.ID, PlanID: p.Hash, State: Queued}
	assessment, err := u.executor.InspectResolution(t.Context(), successor, source, p, u.desired, recoveryEvents("preflight", "stop_unit"))
	if err != nil || assessment.Action != ResumeForward {
		t.Fatal(assessment.Action, err)
	}
	if err := u.executor.Recover(t.Context(), assessment); err != nil {
		t.Fatal(err)
	}
	if u.state != Succeeded || slices.Contains(u.effects, "stop_unit") {
		t.Fatal(u.state, u.effects)
	}
}
