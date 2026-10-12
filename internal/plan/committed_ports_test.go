package plan

import (
	"bytes"
	"strings"
	"testing"

	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/target"
)

func TestCommittedPortWithdrawalChangesDecisionFreshness(t *testing.T) {
	in := committedPortFixture(t)
	withReservation, err := canonicalDecisionFacts(in)
	if err != nil {
		t.Fatal(err)
	}
	(*in.Snapshot.Apps.Value)[0].CommittedPortReservation = nil
	withoutReservation, err := canonicalDecisionFacts(in)
	if err != nil || bytes.Equal(withReservation, withoutReservation) {
		t.Fatal("withdrawn reservation did not invalidate plan freshness", err)
	}
}

func committedPortFixture(t *testing.T) Input {
	t.Helper()
	in, _ := managedPlanInput(t, false)
	previous := installed(t)
	in.Snapshot, in.State = previous.Snapshot, previous.State
	app := &(*in.Snapshot.Apps.Value)[0]
	app.AllocatedHostPort = target.Observation[target.Port]{Status: target.Unknown}
	app.QuadletUnits = target.Known([]target.Unit{})
	head := in.State.Releases[0]
	app.CommittedPortReservation = &target.CommittedPortReservation{ReleaseID: head.ID, Port: head.HostPort}
	return in
}

func TestManagedCommittedPortReservationKeepsRuntimeUnknown(t *testing.T) {
	in := committedPortFixture(t)
	planned, err := Build(in)
	if err != nil || planned.ManagedPreview == nil || planned.ManagedPreview.EngineHostPort != 20001 {
		t.Fatal(planned, err)
	}
	if (*in.Snapshot.Apps.Value)[0].AllocatedHostPort.Status != target.Unknown {
		t.Fatal("planning rewrote runtime observation")
	}
}

func TestManagedCommittedPortReservationRefusesUncertainScope(t *testing.T) {
	tests := []struct {
		name   string
		change func(*Input)
	}{
		{"no reservation", func(in *Input) { (*in.Snapshot.Apps.Value)[0].CommittedPortReservation = nil }},
		{"foreign target", func(in *Input) { in.State.Target.ID = "foreign-target" }},
		{"different generation", func(in *Input) { in.State.Generation++ }},
		{"missing head", func(in *Input) { in.State.Releases = []CurrentRelease{} }},
		{"foreign app", func(in *Input) { (*in.Snapshot.Apps.Value)[0].Name = "foreign" }},
		{"different release", func(in *Input) { (*in.Snapshot.Apps.Value)[0].CommittedPortReservation.ReleaseID = "other-release" }},
		{"different port", func(in *Input) { (*in.Snapshot.Apps.Value)[0].CommittedPortReservation.Port++ }},
		{"remaining units", func(in *Input) { (*in.Snapshot.Apps.Value)[0].QuadletUnits = target.Known(in.State.Releases[0].Units) }},
		{"unknown units", func(in *Input) {
			(*in.Snapshot.Apps.Value)[0].QuadletUnits = target.Observation[[]target.Unit]{Status: target.Unknown}
		}},
		{"unknown listeners", func(in *Input) { in.Snapshot.UsedPorts = target.Observation[[]target.Port]{Status: target.Unknown} }},
		{"unknown owners", func(in *Input) {
			in.Snapshot.PortOwners = target.Observation[[]target.PortOwner]{Status: target.Unknown}
		}},
		{"managed transition", func(in *Input) { in.State.Releases[0].Managed = &ManagedServicePlan{} }},
		{"managed allocation", func(in *Input) {
			in.State.ManagedReservations = []target.ManagedPortReservation{{App: in.State.Releases[0].App, BindingID: data.ReplicaBindingID(strings.Repeat("a", 32)), EnginePort: 20002}}
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			in := committedPortFixture(t)
			test.change(&in)
			planned, err := Build(in)
			if err == nil && planned.ManagedPreview != nil && planned.ManagedPreview.EngineHostPort != 0 {
				t.Fatal("uncertain reservation allowed fresh allocation")
			}
		})
	}
}
