package ops

import (
	"encoding/json"
	"strings"

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

func (r ManagedRelease) Valid() bool {
	if !data.ValidID(string(r.BindingID)) || !data.ValidID(string(r.EpochID)) || !data.ValidCatalogHash(r.RuntimeSHA256) || r.EnginePort < 1024 || r.EnginePort > 65535 || r.EngineUnit.Name != "brine-engine-"+string(r.BindingID)+".service" || !planID.MatchString(r.EngineUnit.Hash) {
		return false
	}
	return r.Frontend == nil || r.Frontend.valid(r.EnginePort)
}

func (f ManagedFrontend) valid(enginePort target.Port) bool {
	name, container := strings.CutSuffix(f.Unit.Name, ".container")
	planned := plan.ManagedFrontendPlan{Image: f.Image, Port: f.Port, Secrets: f.Secrets}
	return container && appName.MatchString(name) && planID.MatchString(f.Unit.Hash) && planned.Valid(enginePort)
}

func (f *ManagedFrontend) UnmarshalJSON(raw []byte) error {
	if len(raw) > 64<<10 {
		return strictjson.ErrObject
	}
	fields, err := strictjson.Object(raw, "image", "port", "unit", "secrets")
	if err != nil {
		return err
	}
	if _, err := strictjson.Object(fields["unit"], "name", "hash"); err != nil {
		return err
	}
	// Reuse the closed frontend planning codec for image and secret validation.
	plannedRaw, err := json.Marshal(map[string]json.RawMessage{"image": fields["image"], "port": fields["port"], "secrets": fields["secrets"]})
	var planned plan.ManagedFrontendPlan
	if err != nil || json.Unmarshal(plannedRaw, &planned) != nil {
		return strictjson.ErrObject
	}
	type record ManagedFrontend
	value, err := strictjson.Value[record](raw)
	if err != nil || !ManagedFrontend(value).valid(0) {
		return strictjson.ErrObject
	}
	*f = ManagedFrontend(value)
	return nil
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
	*r = ManagedRelease(value)
	return nil
}
