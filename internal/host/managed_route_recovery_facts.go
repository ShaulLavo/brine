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
	if !p.Managed.Valid() || facts.Input.Desired.PolicyHash != p.PolicyHash || facts.Input.Desired.PolicyVersion != p.PolicyVersion || !reflect.DeepEqual(facts.Input.State.Target, p.Target) || p.ObservedGeneration.Status != target.KnownStatus || p.ObservedGeneration.Value == nil || *p.ObservedGeneration.Value > ^uint64(0)-2 {
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
		return f.managedUnrecordedRecoveryRoute(ctx, facts, owner, route, err)
	}
	if facts.Input.State.Generation != *p.ObservedGeneration.Value+2 || !managedRecoveryRouteObserved(facts.Input.Snapshot, route) {
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

func (f operationFacts) managedUnrecordedRecoveryRoute(ctx context.Context, facts apply.Facts, owner string, previous store.ManagedRouteState, readErr error) (apply.Facts, error) {
	p := f.recoveryPlan
	fail := errors.New("host: unrecorded managed recovery route changed")
	if facts.Input.State.Generation != *p.ObservedGeneration.Value+1 || readErr != nil && !errors.Is(readErr, store.ErrNotFound) || readErr == nil && previous.Revision != p.ManagedRouteRevision || errors.Is(readErr, store.ErrNotFound) && p.ManagedRouteRevision != 0 {
		return apply.Facts{}, fail
	}
	events, err := f.service.Store.EventsAfter(ctx, owner, 0, 256)
	if err != nil {
		return apply.Facts{}, err
	}
	cursor, valid := ops.ManagedCreateRecoveryCursor(events, p)
	if !valid || cursor.Boundary.Step != "managed_route_receipt" || cursor.Boundary.Outcome == "completed" {
		return apply.Facts{}, fail
	}
	pol, err := f.service.Policy.Load(ctx)
	if err != nil || pol.Hash() != p.PolicyHash {
		return apply.Facts{}, fail
	}
	site, err := caddy.NewManagedSite(facts.Input.Desired, pol, *p.Managed)
	if err != nil {
		return apply.Facts{}, fail
	}
	raw, err := caddy.Render(site)
	observed := facts.Input.Snapshot.CaddyConfig
	if err != nil || observed.Status != target.KnownStatus || observed.Value == nil || observed.Value.Generation == 0 {
		return apply.Facts{}, fail
	}
	name, hash := p.App+".caddy", "sha256:"+managedServiceHash(raw)
	if !managedRecoveryObservedFile(facts.Input.Snapshot, name, hash, observed.Value.Generation) {
		return apply.Facts{}, fail
	}
	facts.Routing.Generation = observed.Value.Generation
	facts.Routing.Files[name], facts.Routing.Sites[name] = hash, site
	return facts, nil
}

func managedRecoveryRouteObserved(snapshot target.Snapshot, route store.ManagedRouteState) bool {
	return managedRecoveryObservedFile(snapshot, route.App+".caddy", route.FileHash, route.Generation)
}
func managedRecoveryObservedFile(snapshot target.Snapshot, name, hash string, generation uint64) bool {
	observed := snapshot.CaddyConfig
	if observed.Status != target.KnownStatus || observed.Value == nil || observed.Value.Generation != generation {
		return false
	}
	found := false
	for _, file := range observed.Value.Files {
		if file.Name != name {
			continue
		}
		if found || file.Hash != hash {
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
