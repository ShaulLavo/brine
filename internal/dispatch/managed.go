package dispatch

import (
	"encoding/json"
	"slices"

	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/spacetimedb"
	"github.com/ShaulLavo/brine/internal/spec"
	"github.com/ShaulLavo/brine/internal/strictjson"
)

const managedMetadataLimit = 4096

// ManagedModuleInput is untrusted desired metadata, never a native observation
// or permission to initialize, publish, or start an engine.
type ManagedModuleInput struct {
	Database data.DatabaseName   `json:"database"`
	Metadata spec.ModuleMetadata `json:"metadata"`
}

func (m ManagedModuleInput) valid() bool {
	return ValidApp(string(m.Database)) &&
		data.ValidCatalogHash(string(m.Metadata.WasmSHA256)) &&
		data.ValidCatalogHash(string(m.Metadata.RuntimeModuleHash)) &&
		m.Metadata.Canonicalizer == spacetimedb.CanonicalizerVersion &&
		data.ValidCatalogHash(m.Metadata.SchemaSHA256)
}

func (m *ManagedModuleInput) UnmarshalJSON(raw []byte) error {
	if len(raw) > managedMetadataLimit {
		return strictjson.ErrObject
	}
	fields, err := strictjson.Object(raw, "database", "metadata")
	if err != nil {
		return err
	}
	metadata, err := strictjson.Object(fields["metadata"], "wasm_sha256", "runtime_module_hash", "canonicalizer", "schema_sha256")
	if err != nil {
		return err
	}
	for _, value := range metadata {
		if _, err := strictjson.Value[string](value); err != nil {
			return err
		}
	}
	type input ManagedModuleInput
	value, err := strictjson.Value[input](raw)
	if err != nil || !ManagedModuleInput(value).valid() {
		return strictjson.ErrObject
	}
	*m = ManagedModuleInput(value)
	return nil
}

// NewPlanArgs owns the resolved metadata. Module paths remain client-local;
// the receiving host never opens the declaration's Wasm or sidecar paths.
func NewPlanArgs(raw string, app spec.App) (PlanArgs, error) {
	if len(raw) > RequestLimit/2 {
		return PlanArgs{}, strictjson.ErrObject
	}
	request := PlanArgs{Spec: raw, SchemaArtifacts: slices.Clone(app.SchemaArtifacts)}
	if app.SpacetimeDB == nil {
		return request, nil
	}
	if spec.ValidateManagedApp(app) != nil || app.SpacetimeDB.Module.Metadata == nil || len(request.SchemaArtifacts) != 0 {
		return PlanArgs{}, strictjson.ErrObject
	}
	input := ManagedModuleInput{Database: app.SpacetimeDB.Name, Metadata: *app.SpacetimeDB.Module.Metadata}
	if !input.valid() {
		return PlanArgs{}, strictjson.ErrObject
	}
	request.SchemaArtifacts = []data.SchemaArtifact{}
	request.ManagedModule = &input
	return request, nil
}

func planFields(raw json.RawMessage, extra ...string) (map[string]json.RawMessage, error) {
	if len(raw) > RequestLimit {
		return nil, strictjson.ErrObject
	}
	var presence map[string]json.RawMessage
	if json.Unmarshal(raw, &presence) != nil {
		return nil, strictjson.ErrObject
	}
	keys := []string{"spec", "schema_artifacts"}
	if _, present := presence["managed_module"]; present {
		keys = append(keys, "managed_module")
	}
	return strictjson.Object(raw, append(keys, extra...)...)
}

func attachManagedModule(app *spec.App, raw json.RawMessage) error {
	if app.SpacetimeDB == nil {
		if raw != nil {
			return strictjson.ErrObject
		}
		return nil
	}
	if len(app.Databases) != 0 || len(app.SchemaArtifacts) != 0 || raw == nil {
		return strictjson.ErrObject
	}
	var input ManagedModuleInput
	if json.Unmarshal(raw, &input) != nil || input.Database != app.SpacetimeDB.Name {
		return strictjson.ErrObject
	}
	metadata := input.Metadata
	app.SpacetimeDB.Module.Metadata = &metadata
	return spec.ValidateManagedApp(*app)
}
