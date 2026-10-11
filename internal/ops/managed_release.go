package ops

import (
	"encoding/json"
	"slices"

	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/plan"
	"github.com/ShaulLavo/brine/internal/strictjson"
	"github.com/ShaulLavo/brine/internal/target"
)

// ManagedRelease references current managed ownership. App rollback never
// restores a historical engine runtime, module, epoch, or signing identity.
type ManagedRelease struct {
	BindingID     data.ReplicaBindingID `json:"binding_id"`
	EpochID       data.ReplicaEpochID   `json:"epoch_id"`
	RuntimeSHA256 string                `json:"runtime_sha256"`
	EngineUnit    target.Unit           `json:"engine_unit"`
	EnginePort    target.Port           `json:"engine_port"`
	Frontend      *ManagedFrontend      `json:"frontend,omitempty"`
}

type ManagedFrontend struct {
	Image   plan.Image           `json:"image"`
	Port    target.Port          `json:"port"`
	Unit    target.Unit          `json:"unit"`
	Secrets []plan.SecretBinding `json:"secrets"`
}

func (r ManagedRelease) Clone() ManagedRelease {
	if r.Frontend != nil {
		frontend := *r.Frontend
		frontend.Secrets = slices.Clone(frontend.Secrets)
		if frontend.Image.ManifestDigest.Value != nil {
			value := *frontend.Image.ManifestDigest.Value
			frontend.Image.ManifestDigest.Value = &value
		}
		r.Frontend = &frontend
	}
	return r
}

func (r ManagedRelease) Valid() bool {
	if !data.ValidID(string(r.BindingID)) || !data.ValidID(string(r.EpochID)) || !data.ValidCatalogHash(r.RuntimeSHA256) || r.EnginePort < 1024 || r.EngineUnit.Name != "brine-engine-"+string(r.BindingID)+".service" || !planID.MatchString(r.EngineUnit.Hash) {
		return false
	}
	return r.Frontend == nil || r.Frontend.Port >= 1024 && r.Frontend.Port != r.EnginePort && planID.MatchString(r.Frontend.Unit.Hash) && r.Frontend.Secrets != nil
}

func (r *ManagedRelease) UnmarshalJSON(raw []byte) error {
	if len(raw) > 64<<10 {
		return strictjson.ErrObject
	}
	var presence map[string]json.RawMessage
	if json.Unmarshal(raw, &presence) != nil {
		return strictjson.ErrObject
	}
	keys := []string{"binding_id", "epoch_id", "runtime_sha256", "engine_unit", "engine_port"}
	if _, present := presence["frontend"]; present {
		keys = append(keys, "frontend")
	}
	fields, err := strictjson.Object(raw, keys...)
	if err != nil {
		return err
	}
	if _, err := strictjson.Object(fields["engine_unit"], "name", "hash"); err != nil {
		return err
	}
	if frontend := fields["frontend"]; frontend != nil {
		if _, err := strictjson.Object(frontend, "image", "port", "unit", "secrets"); err != nil {
			return err
		}
	}
	type record ManagedRelease
	value, err := strictjson.Value[record](raw)
	if err != nil || !ManagedRelease(value).Valid() {
		return strictjson.ErrObject
	}
	*r = ManagedRelease(value).Clone()
	return nil
}
