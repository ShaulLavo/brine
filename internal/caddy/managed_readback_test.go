package caddy

import (
	"context"
	"errors"
	"os"
	"testing"

	"github.com/ShaulLavo/brine/internal/data"
)

type managedLoadedFixture struct {
	raw     []byte
	failure error
	reads   int
}

func (*managedLoadedFixture) Validate(context.Context, string) error          { return nil }
func (f *managedLoadedFixture) Adapt(context.Context, string) ([]byte, error) { return f.raw, nil }
func (f *managedLoadedFixture) ReadLoaded(context.Context) ([]byte, error) {
	f.reads++
	return f.raw, f.failure
}

type managedAuthorityFixture struct {
	calls   int
	failure error
}

func (f *managedAuthorityFixture) CheckManagedIngress(context.Context, string, data.ReplicaBindingID, data.ReplicaEpochID, string) error {
	f.calls++
	return f.failure
}

func TestManagedReadbackRequiresActualLoadedLoggingRouteAndFreshFence(t *testing.T) {
	preview := managedPreview(t, true)
	d, engine, frontend, err := preview.Values()
	if err != nil {
		t.Fatal(err)
	}
	ingress, err := NewManagedClientIngress(preview)
	if err != nil {
		t.Fatal(err)
	}
	authority := &managedAuthorityFixture{}
	site := Site{name: d.Name, domains: d.Domains, port: engine, managed: &managedSite{desired: d, enginePort: engine, appPort: frontend, ingress: ingress, authority: authority}}
	raw, err := os.ReadFile("testdata/managed-adapted.json")
	if err != nil {
		t.Fatal(err)
	}
	validator := &managedLoadedFixture{raw: raw}
	manager := &Manager{validator: validator}
	sites := map[string]Site{"web.caddy": site}
	if err := manager.verifyLoaded(context.Background(), sites, nil); err != nil {
		t.Fatal(err)
	}
	if validator.reads != 1 || authority.calls != 1 {
		t.Fatal("live config or fence bypassed")
	}
	authority.failure = errors.New("held fence")
	if manager.verifyLoaded(context.Background(), sites, nil) == nil {
		t.Fatal("loaded healthy route bypassed fresh held fence")
	}
	authority.failure = nil
	validator.failure = errors.New("readback unavailable")
	if manager.verifyLoaded(context.Background(), sites, nil) == nil {
		t.Fatal("missing live readback treated as candidate proof")
	}
	manager.validator = &fakeValidator{}
	if manager.verifyLoaded(context.Background(), sites, nil) == nil {
		t.Fatal("adapt-only validator admitted managed loaded state")
	}
}
