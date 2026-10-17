package cloudflare

import (
	"testing"

	"github.com/ShaulLavo/brine/internal/cdn"
)

func ExposureNoOpWireFixture(t *testing.T) (*Adapter, cdn.Plan, cdn.Permit, func() int) {
	t.Helper()
	f := newAPIFixture(t)
	before, err := f.adapter.Observe(t.Context(), f.attachment)
	if err != nil {
		t.Fatal(err)
	}
	steps, err := f.adapter.Actions(f.attachment, before)
	if err != nil || len(steps) != 3 {
		t.Fatal("expected initial upload, exposure and route steps", err)
	}
	sample, err := cdn.NewAssetSample(f.attachment.Binding.Cache, "/files/"+string(cdn.Hash([]byte("sample")))[7:]+".bin")
	if err != nil {
		t.Fatal(err)
	}
	p, err := cdn.NewPlan(cdn.PlanningContext{Attachment: f.attachment, PolicyHash: cdn.Hash([]byte("policy")), Initial: []cdn.Observation{before}}, sample, steps, nil)
	if err != nil {
		t.Fatal(err)
	}
	first := fixturePermit(t, f, cdn.PutEdge, cdn.EdgeConfiguration)
	result, err := f.adapter.Execute(t.Context(), first)
	ack, ok := result.(cdn.Acknowledged)
	if err != nil || !ok {
		t.Fatal("upload did not settle", err)
	}
	prior := cdn.OwnedReceipt{Effect: first.Effect, Profile: first.Action.Profile, Attachment: f.attachment, Resources: ack.Resources}
	sealed, err := f.adapter.Seal(t.Context(), f.attachment, steps[1], prior)
	if err != nil || sealed.Action.Expected != sealed.Action.Desired {
		t.Fatal("upload did not leave exposure already disabled", err)
	}
	permit, err := p.NewPermit("operation", 1, sealed.Action)
	if err != nil {
		t.Fatal(err)
	}
	permit.Claim = "claimed-exposure"
	return f.adapter, p, permit, func() int {
		f.mu.Lock()
		defer f.mu.Unlock()
		return len(f.writes)
	}
}
