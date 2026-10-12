package plan

import (
	"encoding/json"
	"errors"
	"slices"

	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/policy"
	"github.com/ShaulLavo/brine/internal/spec"
)

// ManagedClientDesired keeps the selected frontend's configuration/accepts and
// binds its engine declaration to fresh protected current facts. It never turns
// historical Wasm/schema metadata into publication or runtime authority.
func ManagedClientDesired(in Input, client policy.Desired) (policy.Desired, error) {
	if in.Managed == nil || in.Managed.Runtime == nil || in.Managed.Database == nil || client.SpacetimeDB == nil || in.Desired.SpacetimeDB == nil {
		return policy.Desired{}, errors.New("managed current facts required")
	}
	runtime := in.Managed.Runtime
	if runtime.Validate() != nil || runtime.State != data.EngineInitialized || in.Managed.Database.Name != client.SpacetimeDB.Name || in.Managed.Database.Root != client.SpacetimeDB.PersistentRoot || !slices.Contains(client.SpacetimeDB.Module.Accepts, runtime.Module.Schema.Marker) {
		return policy.Desired{}, errors.New("frontend does not accept current engine")
	}
	raw, err := client.CanonicalBytes()
	if err != nil {
		return policy.Desired{}, err
	}
	var desired policy.Desired
	if json.Unmarshal(raw, &desired) != nil {
		return policy.Desired{}, errors.New("managed client desired invalid")
	}
	accepts := slices.Clone(desired.SpacetimeDB.Module.Accepts)
	current := in.Desired.SpacetimeDB.Clone()
	current.Image = spec.ImageReference(runtime.Image)
	current.Version = runtime.Version
	current.Runtime = runtime.Runtime
	current.Resources = spec.SpacetimeResources{MemoryMB: runtime.MemoryMB, PIDsLimit: runtime.PIDsLimit, PagePoolMB: runtime.PagePoolMB, StopTimeoutSeconds: runtime.StopSeconds}
	current.Publisher = spec.SecretReference(runtime.Secrets.Publisher.Reference)
	current.SigningPrivate = spec.SecretReference(runtime.Secrets.SigningPrivate.Reference)
	current.SigningPublic = spec.SecretReference(runtime.Secrets.SigningPublic.Reference)
	current.Auth = spec.SecretReference(runtime.Secrets.AuthPolicy.Reference)
	current.Module.Accepts = accepts
	current.Module.Marker = runtime.Module.Schema.Marker
	current.Module.Metadata = &spec.ModuleMetadata{WasmSHA256: spec.WasmSHA256(runtime.Module.Wasm), RuntimeModuleHash: spec.RuntimeModuleHash(runtime.Module.Program), Canonicalizer: runtime.Module.Schema.Canonicalizer, SchemaSHA256: runtime.Module.Schema.SHA256}
	desired.SpacetimeDB = current
	return desired, nil
}
