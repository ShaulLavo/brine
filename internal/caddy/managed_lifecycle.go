package caddy

import (
	"encoding/json"
	"errors"
	"slices"

	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/plan"
	"github.com/ShaulLavo/brine/internal/policy"
	"github.com/ShaulLavo/brine/internal/spec"
)

// NewManagedSite validates pure route input. The operation adapter separately
// rechecks current startup/fence ownership immediately before publication.
func NewManagedSite(desired policy.Desired, pol policy.Policy, managed plan.ManagedServicePlan) (Site, error) {
	normalized, err := policy.Normalize(desired.App(), pol)
	if err != nil {
		return Site{}, err
	}
	want, err := desired.CanonicalBytes()
	if err != nil {
		return Site{}, err
	}
	got, err := normalized.CanonicalBytes()
	if err != nil || string(want) != string(got) {
		return Site{}, errors.New("managed routing policy changed")
	}
	site, err := CommittedManagedSite(normalized, managed)
	if err != nil {
		return Site{}, err
	}
	site.managed.pending = true
	return site, nil
}

// CommittedManagedSite reconstructs the exact recorded route for comparison
// and withdrawal. It grants no permission to restore a fenced route.
func CommittedManagedSite(desired policy.Desired, managed plan.ManagedServicePlan) (Site, error) {
	if !managed.Valid() || managed.State != data.EngineInitialized || policy.ValidateManagedDesired(desired) != nil || desired.SpacetimeDB.Name != managed.Database.Name || desired.SpacetimeDB.PersistentRoot != managed.Database.Root || desired.SpacetimeDB.BackupDestination != managed.Archive.Destination.Reference {
		return Site{}, errors.New("managed routing ownership required")
	}
	appPort := spec.Port(0)
	if managed.Frontend != nil {
		appPort = spec.Port(managed.Frontend.Port)
	}
	if desired.App().HasAppContainer() != (managed.Frontend != nil) || len(desired.Domains) == 0 {
		return Site{}, errors.New("managed routing components differ")
	}
	raw, err := desired.CanonicalBytes()
	if err != nil {
		return Site{}, err
	}
	var owned policy.Desired
	if json.Unmarshal(raw, &owned) != nil {
		return Site{}, errors.New("managed routing desired input invalid")
	}
	client := &managedSite{desired: owned, enginePort: spec.Port(managed.EnginePort), appPort: appPort, ingress: ClientIngress{prefix: owned.SpacetimeDB.ClientPrefix(), database: owned.SpacetimeDB.Database}}
	client.binding, client.epoch, client.runtimeHash = managed.Database.BindingID, managed.Archive.EpochID, managed.RuntimeSHA256
	return Site{name: owned.Name, domains: slices.Clone(owned.Domains), port: client.enginePort, managed: client}, nil
}
