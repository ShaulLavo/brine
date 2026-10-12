package plan

import (
	"encoding/json"
	"errors"
	"reflect"

	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/target"
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
	if action == StopApp {
		p.Conflicts = managedStopConflicts(p.Conflicts, in.Managed.OwnedNativeState)
		if len(p.Conflicts) == 0 {
			p.Kind = Update
		}
	}
	// Lifecycle uses current owned components; it never installs artifacts from a
	// previous release, publishes a module, creates a release or rotates keys.
	p.Lifecycle = action
	p.Diff = nil
	p.Changes = []Change{}
	intent := &ManagedLifecyclePlan{Engine: current.Managed.Frontend == nil, Frontend: current.Managed.Frontend != nil, Ingress: true, EngineRunning: true, FrontendRunning: current.Managed.Frontend != nil, IngressServing: true}
	if in.Managed.Intent != nil {
		intent.DesiredBeforeRevision = in.Managed.Intent.Revision
	}
	if action == StopApp {
		intent.FrontendRunning = false
		intent.IngressServing = false
		intent.EngineRunning = !intent.Engine && in.Managed.Intent != nil && in.Managed.Intent.Current && in.Managed.Intent.EngineRunning
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

func managedStopConflicts(conflicts []Diagnostic, owned target.Observation[string]) []Diagnostic {
	result := make([]Diagnostic, 0, len(conflicts))
	for _, conflict := range conflicts {
		if conflict.Code == UnknownFacts && (conflict.Field == "managed.native_state" || conflict.Field == "managed.ingress_logging") || conflict.Code == ArtifactDrift && conflict.Field == "managed.fence" {
			continue
		}
		result = append(result, conflict)
	}
	if owned.Status != target.KnownStatus || owned.Value == nil || *owned.Value != "running" && *owned.Value != "stopped" {
		result = append(result, Diagnostic{Code: UnknownFacts, Field: "managed.owned_native_state"})
	}
	return result
}
