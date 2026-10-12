package plan

import (
	"encoding/json"
	"slices"

	"github.com/ShaulLavo/brine/internal/data"
)

// ManagedFacts is a measured control-state projection, not source proof or
// native launch authority. Its canonical content belongs in plan fingerprints.
type ManagedFacts struct {
	EngineImage Image                    `json:"engine_image"`
	Database    *data.SpacetimeDBBinding `json:"database,omitempty"`
	Archive     *data.ArchiveBinding     `json:"archive,omitempty"`
	Runtime     *data.EngineRuntime      `json:"runtime,omitempty"`
	Fences      []data.QuiescenceFence   `json:"fences"`
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
	return f
}

func (f ManagedFacts) CanonicalBytes() ([]byte, error) {
	return json.Marshal(f.Clone())
}
