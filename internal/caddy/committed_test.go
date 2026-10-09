package caddy

import (
	"github.com/ShaulLavo/brine/internal/policy"
	"github.com/ShaulLavo/brine/internal/spec"
	"testing"
)

func TestCommittedSiteValidatesOnlyRoutingAndCopiesDomains(t *testing.T) {
	d := policy.Desired{Name: "hello", Domains: []spec.Domain{"web.example.com"}}
	site, err := CommittedSite(d, 20000)
	if err != nil {
		t.Fatal(err)
	}
	d.Domains[0] = "mutated.example.com"
	raw, err := Render(site)
	if err != nil || string(raw) != "web.example.com {\n\treverse_proxy 127.0.0.1:20000\n\theader X-Content-Type-Options nosniff\n\theader Referrer-Policy no-referrer\n}\n" {
		t.Fatal(string(raw), err)
	}
	for _, d := range []policy.Desired{{Name: "../hello", Domains: []spec.Domain{"web.example.com"}}, {Name: "hello", Domains: []spec.Domain{"web.example.com {\n import /outside\n}"}}, {Name: "hello"}, {Name: "hello", Domains: []spec.Domain{"web.example.com", "web.example.com"}}} {
		if _, err := CommittedSite(d, 20000); err == nil {
			t.Fatal("unsafe committed route", d)
		}
	}
	for _, port := range []spec.Port{0, 1023} {
		if _, err := CommittedSite(d, port); err == nil {
			t.Fatal("unsafe port", port)
		}
	}
}
