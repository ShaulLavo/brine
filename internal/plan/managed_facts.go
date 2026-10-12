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
	EngineImage         Image                      `json:"engine_image"`
	Database            *data.SpacetimeDBBinding   `json:"database,omitempty"`
	Archive             *data.ArchiveBinding       `json:"archive,omitempty"`
	Runtime             *data.EngineRuntime        `json:"runtime,omitempty"`
	Fences              []data.QuiescenceFence     `json:"fences"`
	NativeState         target.Observation[string] `json:"native_state"`
	LoggingPrerequisite target.Observation[bool]   `json:"logging_prerequisite"`
	Preparation         *ManagedServicePlan        `json:"preparation,omitempty"`
	Prepared            target.Observation[bool]   `json:"prepared"`
	LoggingConfigSHA256 string                     `json:"logging_config_sha256"`
	LoggingRootSHA256   string                     `json:"logging_root_sha256"`
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
	if f.LoggingPrerequisite.Value != nil {
		value := *f.LoggingPrerequisite.Value
		f.LoggingPrerequisite.Value = &value
	}
	if f.Preparation != nil {
		value := f.Preparation.Clone()
		f.Preparation = &value
	}
	if f.Prepared.Value != nil {
		value := *f.Prepared.Value
		f.Prepared.Value = &value
	}
	return f
}

func (f ManagedFacts) CanonicalBytes() ([]byte, error) {
	return json.Marshal(f.Clone())
}
