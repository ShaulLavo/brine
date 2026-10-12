package plan

import (
	"encoding/json"
	"slices"

	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/target"
)

// ManagedFacts is a measured control-state projection, not source proof or
// native launch authority. Its canonical content belongs in plan fingerprints.
type ManagedFacts struct {
	EngineImage      Image                      `json:"engine_image"`
	Database         *data.SpacetimeDBBinding   `json:"database,omitempty"`
	Archive          *data.ArchiveBinding       `json:"archive,omitempty"`
	Runtime          *data.EngineRuntime        `json:"runtime,omitempty"`
	Fences           []data.QuiescenceFence     `json:"fences"`
	NativeState      target.Observation[string] `json:"native_state"`
	IngressProtected target.Observation[bool]   `json:"ingress_protected"`
}

func (f ManagedFacts) Clone() ManagedFacts {
	f.EngineImage = cloneManagedImage(f.EngineImage)
	if f.Database != nil {
		value := *f.Database
		f.Database = &value
	}
	if f.Archive != nil {
		value := *f.Archive
		f.Archive = &value
	}
	if f.Runtime != nil {
		value := f.Runtime.Clone()
		f.Runtime = &value
	}
	f.Fences = slices.Clone(f.Fences)
	if f.NativeState.Value != nil {
		value := *f.NativeState.Value
		f.NativeState.Value = &value
	}
	if f.IngressProtected.Value != nil {
		value := *f.IngressProtected.Value
		f.IngressProtected.Value = &value
	}
	return f
}

func (f ManagedFacts) CanonicalBytes() ([]byte, error) {
	return json.Marshal(f.Clone())
}
