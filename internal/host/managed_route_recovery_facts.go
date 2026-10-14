package host

import (
	"context"
	"errors"
	"reflect"
	"time"

	"github.com/ShaulLavo/brine/internal/apply"
	"github.com/ShaulLavo/brine/internal/caddy"
	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/ops"
	"github.com/ShaulLavo/brine/internal/plan"
	"github.com/ShaulLavo/brine/internal/store"
	"github.com/ShaulLavo/brine/internal/target"
)

// Recovery observes the original applied route from its protected receipt and
// current inventory. Ordinary planning still reconstructs only committed sites.
func (f operationFacts) managedRecoveryRoutes(ctx context.Context, facts apply.Facts) (apply.Facts, error) {
	p := f.recoveryPlan
	if p.Managed == nil || p.Managed.State != data.EngineInitialized || p.Kind != plan.Create || p.Lifecycle != "" {
		return facts, nil
	}
	for _, release := range facts.Input.State.Releases {
		if release.App == p.App {
			return facts, nil
		}
	}
	fail := func() (apply.Facts, error) {
		return apply.Facts{}, errors.New("host: managed recovery route proof changed")
	}
	if !p.Managed.Valid() || facts.Input.Desired.PolicyHash != p.PolicyHash || facts.Input.Desired.PolicyVersion != p.PolicyVersion || !reflect.DeepEqual(facts.Input.State.Target, p.Target) || p.ObservedGeneration.Status != target.KnownStatus || p.ObservedGeneration.Value == nil || *p.ObservedGeneration.Value > ^uint64(0)-2 || facts.Input.State.Generation != *p.ObservedGeneration.Value+2 {
		return fail()
	}
	owner, err := f.managedRecoveryOwner(ctx)
	if err != nil {
		return apply.Facts{}, err
	}
	native, err := f.service.Store.ReadEnginePermit(ctx, p.Managed.Database.BindingID)
	if err != nil || native.Reserved.Database != p.Managed.Database || native.Reserved.Archive != p.Managed.Archive || !native.AllowsVerification(time.Now().UTC()) {
		return fail()
	}
	hash, err := managedRuntimeHash(native.Runtime)
	if err != nil || hash != p.Managed.RuntimeSHA256 {
		return fail()
	}
	desired, err := currentManagedDesired(ctx, f.service.Store, native.Runtime)
	if err != nil || desired.OperationID != owner || desired.PlanID != p.Hash || desired.Revision != p.ManagedIntentRevision+1 || !desired.EngineRunning || !desired.IngressServing || desired.FrontendRunning != (p.Managed.Frontend != nil) {
		return fail()
	}
	route, err := f.service.Store.ReadManagedRouteState(ctx, p.App)
	if err != nil || !route.Valid() || !route.Present || route.OperationID != owner || route.PlanID != p.Hash || route.BindingID != desired.BindingID || route.EpochID != desired.EpochID || route.RuntimeSHA256 != desired.RuntimeSHA256 || route.DesiredRevision != desired.Revision || route.Revision != p.ManagedRouteRevision+1 {
		return fail()
	}
	if !managedRecoveryRouteObserved(facts.Input.Snapshot, route) {
		return fail()
	}
	pol, err := f.service.Policy.Load(ctx)
	if err != nil || pol.Hash() != p.PolicyHash {
		return fail()
	}
	site, err := caddy.NewManagedSite(facts.Input.Desired, pol, *p.Managed)
	if err != nil {
		return fail()
	}
	name := p.App + ".caddy"
	facts.Routing.Generation = route.Generation
	facts.Routing.Files[name] = route.FileHash
	facts.Routing.Sites[name] = site
	return facts, nil
}
func managedRecoveryRouteObserved(snapshot target.Snapshot, route store.ManagedRouteState) bool {
	observed := snapshot.CaddyConfig
	if observed.Status != target.KnownStatus || observed.Value == nil || observed.Value.Generation != route.Generation {
		return false
	}
	found := false
	for _, file := range observed.Value.Files {
		if file.Name != route.App+".caddy" {
			continue
		}
		if found || file.Hash != route.FileHash {
			return false
		}
		found = true
	}
	return found
}
func (f operationFacts) managedRecoveryOwner(ctx context.Context) (string, error) {
	p, current := f.recoveryPlan, f.recoveryOperation
	seen := map[string]bool{}
	for depth := 0; depth < 64; depth++ {
		if seen[current.ID] || current.PlanID != p.Hash || current.App != p.App {
			return "", store.ErrConflict
		}
		seen[current.ID] = true
		if current.Kind == ops.Deploy {
			return current.ID, nil
		}
		if current.Kind != ops.Resolve {
			return "", store.ErrConflict
		}
		parent, err := f.service.Store.GetOperation(ctx, current.RecoveryOf)
		if err != nil || parent.State != ops.RecoveryRequired || parent.SecretRef != current.SecretRef {
			return "", store.ErrConflict
		}
		current = parent
	}
	return "", store.ErrConflict
}
