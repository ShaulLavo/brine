package spec

import (
	"path/filepath"

	"github.com/ShaulLavo/brine/internal/spacetimedb"
)

func (app *App) resolveManagedFiles(directory string) error {
	d := app.SpacetimeDB
	wasm, err := readRelativeFile(directory, filepath.FromSlash(d.Module.File), MaxWasmBytes)
	if err != nil {
		return refusal("spec.module_file", "databases.spacetimedb.module.file", "cannot read a bounded regular Wasm file")
	}
	artifact, native, err := moduleIdentities(wasm)
	if err != nil {
		return refusal("spec.module_file", "databases.spacetimedb.module.file", "expected a supported Wasm module")
	}
	limits := spacetimedb.DefaultSchemaLimits()
	raw, err := readRelativeFile(directory, filepath.FromSlash(d.Module.Schema), limits.MaxBytes)
	if err != nil {
		return refusal("spec.module_schema", "databases.spacetimedb.module.schema", "cannot read a bounded regular schema sidecar")
	}
	schema, err := spacetimedb.CanonicalSchemaV10(raw, limits)
	if err != nil {
		return refusal("spec.module_schema", "databases.spacetimedb.module.schema", "expected a supported SpacetimeDB v10 schema")
	}
	d.Module.Metadata = &ModuleMetadata{WasmSHA256: artifact, RuntimeModuleHash: native, Canonicalizer: schema.Version(), SchemaSHA256: schema.SHA256()}
	return nil
}
