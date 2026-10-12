package spec

import (
	"regexp"
	"slices"
	"strings"

	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/spacetimedb"
)

type WasmSHA256 string
type RuntimeModuleHash string

// ModuleMetadata is untrusted desired input, never observation or startup proof.
type ModuleMetadata struct {
	WasmSHA256        WasmSHA256        `json:"wasm_sha256"`
	RuntimeModuleHash RuntimeModuleHash `json:"runtime_module_hash"`
	Canonicalizer     string            `json:"canonicalizer"`
	SchemaSHA256      string            `json:"schema_sha256"`
}
type SpacetimeModule struct {
	File     string          `json:"file" toml:"file"`
	Schema   string          `json:"schema" toml:"schema"`
	Marker   string          `json:"marker" toml:"marker"`
	Accepts  []string        `json:"accepts" toml:"accepts"`
	Metadata *ModuleMetadata `json:"metadata,omitempty" toml:"-"`
}

var engineSecretReference = regexp.MustCompile(`^[a-z][a-z0-9-]{0,62}$`)

type SpacetimeResources struct {
	MemoryMB           int `json:"memory_mb" toml:"memory_mb"`
	PIDsLimit          int `json:"pids_limit" toml:"pids_limit"`
	PagePoolMB         int `json:"page_pool_mb" toml:"page_pool_mb"`
	StopTimeoutSeconds int `json:"stop_timeout_seconds" toml:"stop_timeout_seconds"`
}
type SpacetimeDB struct {
	Name                data.DatabaseName         `json:"name" toml:"name"`
	PersistentRoot      data.PersistentRoot       `json:"persistent_root" toml:"persistent_root"`
	BackupDestination   data.BackupDestinationRef `json:"backup_destination" toml:"backup_destination"`
	RecoveryPasswordRef SecretReference           `json:"recovery_password_ref" toml:"recovery_password_ref"`
	Image               ImageReference            `json:"image" toml:"image"`
	Version             string                    `json:"version" toml:"version"`
	Database            string                    `json:"database" toml:"database"`
	Module              SpacetimeModule           `json:"module" toml:"module"`
	Runtime             data.RuntimeIdentity      `json:"runtime" toml:"runtime"`
	Resources           SpacetimeResources        `json:"resources" toml:"resources"`
	Publisher           SecretReference           `json:"publisher" toml:"publisher"`
	SigningPrivate      SecretReference           `json:"signing_private" toml:"signing_private"`
	SigningPublic       SecretReference           `json:"signing_public" toml:"signing_public"`
	Auth                SecretReference           `json:"auth" toml:"auth"`
}

type rawSpacetimeDB struct {
	Image          any                    `toml:"image"`
	Version        any                    `toml:"version"`
	Database       any                    `toml:"database"`
	Module         *rawSpacetimeModule    `toml:"module"`
	Runtime        *rawRuntime            `toml:"runtime"`
	Resources      *rawSpacetimeResources `toml:"resources"`
	Publisher      any                    `toml:"publisher"`
	SigningPrivate any                    `toml:"signing_private"`
	SigningPublic  any                    `toml:"signing_public"`
	Auth           any                    `toml:"auth"`
}
type rawSpacetimeModule struct {
	File   any `toml:"file"`
	Schema any `toml:"schema"`
	Marker any `toml:"marker"`
}
type rawSpacetimeResources struct {
	MemoryMB           any `toml:"memory_mb"`
	PIDsLimit          any `toml:"pids_limit"`
	PagePoolMB         any `toml:"page_pool_mb"`
	StopTimeoutSeconds any `toml:"stop_timeout_seconds"`
}

func validateAppContainer(raw rawApp, app *App) error {
	if managedRawDatabase(raw) != nil && raw.Image == nil && raw.ContainerPort == nil {
		if raw.Resources != nil || raw.Health != nil || len(raw.Environment) != 0 || len(raw.Secrets) != 0 {
			return refusal("spec.invalid_managed", "image", "app settings require an application container")
		}
		return nil
	}
	image, err := text(raw.Image, "image")
	if err != nil {
		return err
	}
	normalized, ok := imageReference(image)
	if !ok {
		return refusal("spec.invalid_image", "image", "expected an explicit OCI registry and repository pinned to a sha256 digest")
	}
	port, err := integer(raw.ContainerPort, "container_port")
	if err != nil {
		return err
	}
	if port < 1024 || port > 65535 {
		return refusal("spec.invalid_port", "container_port", "port must be between 1024 and 65535")
	}
	app.Image, app.ContainerPort = ImageReference(normalized), Port(port)
	return nil
}

func validateManaged(raw rawApp, db *rawDatabase, app *App) error {
	r := db.SpacetimeDB
	if r == nil {
		return refusal("spec.invalid_managed", "databases.spacetimedb", "managed service payload is required")
	}
	bad := func() error {
		return refusal("spec.invalid_managed", "databases.spacetimedb", "invalid managed SpacetimeDB declaration")
	}
	if len(raw.Databases) != 1 || raw.Runtime != nil || db.MountPath != nil || db.Filename != nil || db.SyncInterval != nil || db.Schema != nil || db.Marker != nil || len(raw.RestoreInvariants) != 0 || r.Runtime == nil || r.Resources == nil || r.Module == nil {
		return bad()
	}
	var d SpacetimeDB
	fields := []struct {
		raw any
		set func(string)
	}{
		{db.Name, func(v string) { d.Name = data.DatabaseName(v) }},
		{db.PersistentRoot, func(v string) { d.PersistentRoot = data.PersistentRoot(v) }},
		{db.BackupDestination, func(v string) { d.BackupDestination = data.BackupDestinationRef(v) }},
		{db.RecoveryPasswordRef, func(v string) { d.RecoveryPasswordRef = SecretReference(v) }},
		{r.Image, func(v string) { d.Image = ImageReference(v) }},
		{r.Version, func(v string) { d.Version = v }},
		{r.Database, func(v string) { d.Database = v }},
		{r.Publisher, func(v string) { d.Publisher = SecretReference(v) }},
		{r.SigningPrivate, func(v string) { d.SigningPrivate = SecretReference(v) }},
		{r.SigningPublic, func(v string) { d.SigningPublic = SecretReference(v) }},
		{r.Auth, func(v string) { d.Auth = SecretReference(v) }},
		{r.Module.File, func(v string) { d.Module.File = v }},
		{r.Module.Schema, func(v string) { d.Module.Schema = v }},
		{r.Module.Marker, func(v string) { d.Module.Marker = v }},
	}
	for _, field := range fields {
		v, err := text(field.raw, "spacetimedb")
		if err != nil {
			return err
		}
		field.set(v)
	}
	if err := managedNumbers(r, &d); err != nil {
		return bad()
	}
	names := map[DatabaseName]bool{d.Name: true}
	if err := validateCompatibility(raw.SchemaCompatibility, names, app); err != nil {
		return err
	}
	if len(app.SchemaCompatibility) == 0 {
		app.SchemaCompatibility = []SchemaCompatibility{{Database: d.Name, Accepts: []string{d.Module.Marker}, Startup: "preserve"}}
	}
	d.Module.Accepts = slices.Clone(app.SchemaCompatibility[0].Accepts)
	image, ok := imageReference(string(d.Image))
	if !ok {
		return bad()
	}
	d.Image = ImageReference(image)
	slices.Sort(d.Module.Accepts)
	if d.Validate() != nil {
		return bad()
	}
	app.SpacetimeDB = &d
	return nil
}

func managedNumbers(r *rawSpacetimeDB, d *SpacetimeDB) error {
	uid, err := managedRuntimeID(r.Runtime.UID)
	if err != nil {
		return err
	}
	gid, err := managedRuntimeID(r.Runtime.GID)
	if err != nil {
		return err
	}
	d.Runtime = data.RuntimeIdentity{UID: uid, GID: gid}
	fields := []struct {
		raw any
		set func(int64)
	}{
		{r.Resources.MemoryMB, func(v int64) { d.Resources.MemoryMB = int(v) }},
		{r.Resources.PIDsLimit, func(v int64) { d.Resources.PIDsLimit = int(v) }},
		{r.Resources.PagePoolMB, func(v int64) { d.Resources.PagePoolMB = int(v) }},
		{r.Resources.StopTimeoutSeconds, func(v int64) { d.Resources.StopTimeoutSeconds = int(v) }},
	}
	for _, field := range fields {
		v, err := integer(field.raw, "spacetimedb")
		if err != nil || v < 1 || v > 2147483647 {
			return data.ErrInvalid
		}
		field.set(v)
	}
	return nil
}

func managedRuntimeID(raw any) (uint32, error) {
	v, err := integer(raw, "databases.spacetimedb.runtime")
	if err != nil || v < 1 || v > 2147483647 {
		return 0, data.ErrInvalid
	}
	return uint32(v), nil
}

func (d SpacetimeDB) Declaration() (data.EngineDeclaration, error) {
	return data.SpacetimeDBDeclaration(data.SpacetimeDBDatabase{Name: d.Name, PersistentRoot: d.PersistentRoot, BackupDestination: d.BackupDestination})
}

func (d SpacetimeDB) Validate() error {
	_, err := d.Declaration()
	image, ok := imageReference(string(d.Image))
	if err != nil || !ok || image != string(d.Image) || d.Version != "2.11.0" || !label.MatchString(d.Database) || len(d.Database) > 63 || d.Runtime.Validate() != nil {
		return data.ErrInvalid
	}
	r := d.Resources
	if r.MemoryMB < 1 || r.MemoryMB > 2147483647 || r.PIDsLimit < 1 || r.PIDsLimit > 2147483647 || r.PagePoolMB < 1 || r.PagePoolMB > r.MemoryMB || r.StopTimeoutSeconds < 1 || r.StopTimeoutSeconds > MaxStartupDeadlineSeconds {
		return data.ErrInvalid
	}
	if !validSchemaPath(d.Module.File) || !validSchemaPath(d.Module.Schema) || d.Module.File == d.Module.Schema || !data.ValidMarker(d.Module.Marker) || d.Module.Marker == data.EmptyMarker || len(d.Module.Accepts) == 0 || len(d.Module.Accepts) > 128 {
		return data.ErrInvalid
	}
	seen := map[string]bool{}
	for _, marker := range d.Module.Accepts {
		if !data.ValidMarker(marker) || marker == data.EmptyMarker || seen[marker] {
			return data.ErrInvalid
		}
		seen[marker] = true
	}
	if !seen[d.Module.Marker] {
		return data.ErrInvalid
	}
	if !engineSecretReference.MatchString(string(d.RecoveryPasswordRef)) {
		return data.ErrInvalid
	}
	refs := d.References()
	seenRefs := map[SecretReference]bool{}
	for _, ref := range refs {
		if !engineSecretReference.MatchString(string(ref)) || seenRefs[ref] {
			return data.ErrInvalid
		}
		seenRefs[ref] = true
	}
	if m := d.Module.Metadata; m != nil {
		if !data.ValidCatalogHash(string(m.WasmSHA256)) || !data.ValidCatalogHash(string(m.RuntimeModuleHash)) || !data.ValidCatalogHash(m.SchemaSHA256) || m.Canonicalizer != spacetimedb.CanonicalizerVersion {
			return data.ErrInvalid
		}
	}
	return nil
}

func (d SpacetimeDB) Clone() *SpacetimeDB {
	copy := d
	copy.Module.Accepts = slices.Clone(d.Module.Accepts)
	if d.Module.Metadata != nil {
		m := *d.Module.Metadata
		copy.Module.Metadata = &m
	}
	return &copy
}

func (d SpacetimeDB) ClientPrefix() string { return "/_brine/db/" + string(d.Name) + "/" }
func (d SpacetimeDB) EnvironmentPrefix() string {
	return "BRINE_DB_" + data.DatabaseEnvironmentName(d.Name)
}
func (app App) HasAppContainer() bool { return app.Image != "" && app.ContainerPort != 0 }
func (d SpacetimeDB) References() []SecretReference {
	return []SecretReference{d.Publisher, d.SigningPrivate, d.SigningPublic, d.Auth, d.RecoveryPasswordRef}
}
func (d SpacetimeDB) SortedAccepts() string {
	values := slices.Clone(d.Module.Accepts)
	slices.Sort(values)
	return strings.Join(values, ",")
}

func managedRawDatabase(raw rawApp) *rawDatabase {
	for _, db := range raw.Databases {
		if db == nil {
			continue
		}
		engine, ok := db.Engine.(string)
		if ok && engine == string(data.EngineSpacetimeDB) {
			return db
		}
	}
	return nil
}
