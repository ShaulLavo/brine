package plan

import (
	"bytes"
	"os"
	"strings"
	"testing"

	"github.com/ShaulLavo/brine/internal/policy"
	"github.com/ShaulLavo/brine/internal/spec"
	"github.com/ShaulLavo/brine/internal/target"
)

func managedPlanInput(t testing.TB, frontend bool) (Input, policy.Policy) {
	t.Helper()
	in := fixture(t, "fresh-arm64")
	raw, err := os.ReadFile("../spec/testdata/managed.toml")
	if err != nil {
		t.Fatal(err)
	}
	if frontend {
		raw = []byte("image=\"registry.example.com:5000/team/web@sha256:" + strings.Repeat("a", 64) + "\"\ncontainer_port=8080\nresources={memory_mb=128,pids_limit=128}\n" + string(raw))
	}
	a, err := spec.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	raw, err = os.ReadFile("../policy/testdata/managed-operator.toml")
	if err != nil {
		t.Fatal(err)
	}
	pol, err := policy.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	in.Desired, err = policy.Normalize(a, pol)
	if err != nil {
		t.Fatal(err)
	}
	return in, pol
}

func TestManagedPlansAreDeterministicNonAdmittedPreviews(t *testing.T) {
	for _, frontend := range []bool{false, true} {
		in, _ := managedPlanInput(t, frontend)
		in.Snapshot.UsedPorts = target.Known([]target.Port{20000})
		in.Snapshot.PortOwners = target.Known([]target.PortOwner{{Port: 20002, App: "other", Process: "fixture", Unit: "other.service"}})
		p, err := Build(in)
		if err != nil {
			t.Fatal(err)
		}
		if p.Kind != Conflict || len(p.Changes) != 0 || p.ManagedPreview == nil || p.Conflicts[0].Code != ManagedEngineUnsupported {
			t.Fatal("managed preview became executable")
		}
		if p.ManagedPreview.EngineHostPort != 20001 {
			t.Fatal("engine port ignored current ownership")
		}
		if frontend && p.ManagedPreview.AppHostPort != 20003 || !frontend && p.ManagedPreview.AppHostPort != 0 {
			t.Fatal("frontend port differs")
		}
		second, err := Build(in)
		if err != nil {
			t.Fatal(err)
		}
		a, _ := p.CanonicalBytes()
		b, _ := second.CanonicalBytes()
		if !bytes.Equal(a, b) {
			t.Fatal("managed preview is nondeterministic")
		}
	}
}

func TestManagedPreviewPortAndOwnershipRefusals(t *testing.T) {
	in, pol := managedPlanInput(t, true)
	if _, err := NewManagedRenderPreview(in.Desired, pol, 20001, 20001); err == nil {
		t.Fatal("same engine/frontend port accepted")
	}
	if _, err := NewManagedRenderPreview(in.Desired, pol, 19999, 20001); err == nil {
		t.Fatal("foreign port range accepted")
	}
	preview, err := NewManagedRenderPreview(in.Desired, pol, 20001, 20003)
	if err != nil {
		t.Fatal(err)
	}
	d, _, _, err := preview.Values()
	if err != nil {
		t.Fatal(err)
	}
	d.SpacetimeDB.Publisher = "foreign"
	if _, err := NewManagedRenderPreview(d, pol, 20001, 20003); err == nil {
		t.Fatal("crossed publisher ownership accepted")
	}
	d, _, _, _ = preview.Values()
	d.SpacetimePolicy.LicenseAuthority = "forged"
	if _, err := NewManagedRenderPreview(d, pol, 20001, 20003); err == nil {
		t.Fatal("forged reviewed license accepted")
	}
	in.Snapshot.UsedPorts = target.Observation[[]target.Port]{Status: target.Unknown}
	p, err := Build(in)
	if err != nil || p.ManagedPreview.EngineHostPort != 0 || len(p.Conflicts) < 2 {
		t.Fatal("unknown ports were treated as free", err)
	}
}
