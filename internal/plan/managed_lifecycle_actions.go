package plan

import (
	"encoding/json"
	"errors"
	"reflect"

	"github.com/ShaulLavo/brine/internal/data"
)

func buildManagedLifecycle(in Input, action ChangeKind) (Plan, error) {
	if action != StartApp && action != StopApp && action != RestartApp || in.Managed == nil || in.Managed.Runtime == nil || in.Managed.Runtime.State != data.EngineInitialized {
		return Plan{}, errors.New("managed initialized lifecycle required")
	}
	var current *CurrentRelease
	for i := range in.State.Releases {
		if in.State.Releases[i].App == string(in.Desired.Name) {
			current = &in.State.Releases[i]
		}
	}
	if current == nil || current.Managed == nil {
		return Plan{}, errors.New("managed lifecycle cannot change configuration")
	}
	currentDesired, err := ManagedClientDesired(in, current.Desired)
	if err != nil {
		return Plan{}, err
	}
	requested, err := ManagedClientDesired(in, in.Desired)
	if err != nil || !reflect.DeepEqual(currentDesired.App(), requested.App()) {
		return Plan{}, errors.New("managed lifecycle cannot change configuration")
	}
	in.Desired = requested
	p, err := Build(in)
	if err != nil {
		return Plan{}, err
	}
	// Lifecycle uses current owned components; it never installs artifacts from a
	// previous release, publishes a module, creates a release or rotates keys.
	p.Lifecycle = action
	p.Diff = nil
	p.Changes = []Change{}
	intent := &ManagedLifecyclePlan{Engine: current.Managed.Frontend == nil, Frontend: current.Managed.Frontend != nil, Ingress: action == StopApp, EngineRunning: true, FrontendRunning: current.Managed.Frontend != nil, IngressServing: true}
	if in.Managed.Intent != nil {
		intent.DesiredBeforeRevision = in.Managed.Intent.Revision
	}
	if action == StopApp {
		intent.FrontendRunning = false
		intent.IngressServing = false
		if intent.Engine {
			intent.EngineRunning = false
		}
	}
	p.ManagedLifecycle = intent
	if p.Kind != Conflict {
		p.Kind = Update
		p.Changes = []Change{{Kind: action}}
	}
	desired, err := in.Desired.CanonicalBytes()
	if err != nil {
		return Plan{}, err
	}
	snapshot, err := canonicalDecisionFacts(in)
	if err != nil {
		return Plan{}, err
	}
	managed, err := in.Managed.CanonicalBytes()
	if err != nil {
		return Plan{}, err
	}
	snapshot, err = json.Marshal(struct {
		Snapshot json.RawMessage `json:"snapshot"`
		Managed  json.RawMessage `json:"managed"`
	}{snapshot, managed})
	if err != nil {
		return Plan{}, err
	}
	state, err := canonicalState(in.State)
	if err != nil {
		return Plan{}, err
	}
	p.Hash = ""
	return finish(p, desired, snapshot, state)
}
