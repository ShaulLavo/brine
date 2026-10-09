//go:build linux && pi_integration

package integration

import (
	"testing"

	"github.com/ShaulLavo/brine/internal/caddy"
	"github.com/ShaulLavo/brine/internal/plan"
	"github.com/ShaulLavo/brine/internal/target"
)

func TestFixtureInventoryBindings(t *testing.T) {
	for _, change := range []string{"matching", "unit hash", "secret ID", "Caddy hash", "port", "image digest", "image platform", "unknown image", "missing app"} {
		t.Run(change, func(t *testing.T) {
			r := receipt{UnitHash: "unit-hash", Plan: plan.Plan{Image: plan.Image{Digest: "index-digest", Platform: target.Platform{OS: "linux", Arch: "arm64"}}, HostPort: 20080, Secrets: []plan.SecretBinding{{VersionName: secretName, ID: "secret-id"}}}, Caddy: caddy.State{Generation: 3, Files: map[string]string{"fixture.caddy": "caddy-hash"}}}
			s := target.Snapshot{
				Apps: target.Known([]target.App{{Name: fixtureName,
					Image:             target.Known(target.Image{Digest: r.Plan.Image.Digest, Platform: r.Plan.Image.Platform}),
					QuadletUnits:      target.Known([]target.Unit{{Name: "fixture.container", Hash: r.UnitHash}}),
					Secrets:           target.Known([]target.Secret{{Name: secretName, ID: "secret-id"}}),
					AllocatedHostPort: target.Known(r.Plan.HostPort),
				}}),
				CaddyConfig: target.Known(target.CaddyConfigSet{Generation: 3, Files: []target.CaddyFile{{Name: "fixture.caddy", Hash: "caddy-hash"}}}),
			}
			switch change {
			case "image digest":
				(*s.Apps.Value)[0].Image.Value.Digest = "drift"
			case "image platform":
				(*s.Apps.Value)[0].Image.Value.Platform.Arch = "amd64"
			case "unknown image":
				(*s.Apps.Value)[0].Image = target.Observation[target.Image]{Status: target.Unknown}
			case "unit hash":
				(*(*s.Apps.Value)[0].QuadletUnits.Value)[0].Hash = "drift"
			case "secret ID":
				(*(*s.Apps.Value)[0].Secrets.Value)[0].ID = "drift"
			case "Caddy hash":
				s.CaddyConfig.Value.Files[0].Hash = "drift"
			case "port":
				(*s.Apps.Value)[0].AllocatedHostPort = target.Known(target.Port(20081))
			case "missing app":
				s.Apps = target.Known([]target.App{})
			}
			if err := validateFixtureInventory(s, r); (err == nil) != (change == "matching") {
				t.Fatalf("unexpected binding validation result: %v", err)
			}
		})
	}
}
