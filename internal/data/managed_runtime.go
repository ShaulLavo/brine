package data

import (
	"encoding/json"
	"slices"
	"time"

	"github.com/ShaulLavo/brine/internal/strictjson"
)

type NativeDatabaseIdentity string
type NativePublisherIdentity string
type NativeProgramWireHash string
type WasmSHA256 string

type EngineSchemaDefinition struct {
	Marker        string `json:"marker"`
	Canonicalizer string `json:"canonicalizer"`
	SHA256        string `json:"sha256"`
}

type EngineModuleDefinition struct {
	Program NativeProgramWireHash  `json:"program_wire_hash"`
	Wasm    WasmSHA256             `json:"wasm_sha256"`
	Schema  EngineSchemaDefinition `json:"schema"`
}

func (s EngineSchemaDefinition) Validate() error {
	if !ValidMarker(s.Marker) || s.Marker == EmptyMarker || s.Canonicalizer != "spacetimedb-v10-expanded-v2" || !ValidCatalogHash(s.SHA256) {
		return ErrInvalid
	}
	return nil
}

func (m EngineModuleDefinition) Validate() error {
	if !ValidCatalogHash(string(m.Program)) || !ValidCatalogHash(string(m.Wasm)) || m.Schema.Validate() != nil {
		return ErrInvalid
	}
	return nil
}

type EngineSecretVersion struct {
	Reference string    `json:"reference"`
	Version   uint64    `json:"version"`
	SHA256    string    `json:"sha256"`
	ExpiresAt time.Time `json:"expires_at"`
}

func (s EngineSecretVersion) Validate() error {
	if !namePattern.MatchString(s.Reference) || s.Version == 0 || !ValidCatalogHash(s.SHA256) {
		return ErrInvalid
	}
	return nil
}

type EngineSecrets struct {
	Publisher      EngineSecretVersion `json:"publisher"`
	SigningPrivate EngineSecretVersion `json:"signing_private"`
	SigningPublic  EngineSecretVersion `json:"signing_public"`
	AuthPolicy     EngineSecretVersion `json:"auth_policy"`
}

type EngineDataState string

const (
	EngineUnpublished EngineDataState = "unpublished"
	EngineInitialized EngineDataState = "initialized"
)

type EngineRuntime struct {
	BindingID      ReplicaBindingID        `json:"binding_id"`
	EpochID        ReplicaEpochID          `json:"epoch_id"`
	State          EngineDataState         `json:"state"`
	Image          string                  `json:"image"`
	ManifestDigest string                  `json:"manifest_digest"`
	ImageID        string                  `json:"image_id"`
	Version        string                  `json:"version"`
	Runtime        RuntimeIdentity         `json:"runtime"`
	HostPort       uint16                  `json:"host_port"`
	Database       NativeDatabaseIdentity  `json:"database_identity"`
	Publisher      NativePublisherIdentity `json:"publisher_identity"`
	Names          []string                `json:"names"`
	Module         EngineModuleDefinition  `json:"module"`
	Secrets        EngineSecrets           `json:"secrets"`
	MemoryMB       int                     `json:"memory_mb"`
	PIDsLimit      int                     `json:"pids_limit"`
	PagePoolMB     int                     `json:"page_pool_mb"`
	StopSeconds    int                     `json:"stop_seconds"`
	StartupSeconds int                     `json:"startup_seconds"`
	CopyBytes      int64                   `json:"copy_bytes"`
	CopyFiles      int                     `json:"copy_files"`
	UnitSHA256     string                  `json:"unit_sha256"`
}

const SpacetimeDBImage = "docker.io/clockworklabs/spacetime@sha256:088aeefda7397bc265876e27fa254cbff64b8563dcc8d28db98423176c64af63"
const SpacetimeDBAMD64Manifest = "sha256:9fb3048daa9fa68ddfe6418ee680e0171761770caca6e0dff7595cda9366f462"
const SpacetimeDBARM64Manifest = "sha256:c4a7048f9ff3981980b97f0119c6221ac51c0a7b78e2d4447f0ee1ee22bae6fa"

func (r EngineRuntime) Clone() EngineRuntime {
	r.Names = slices.Clone(r.Names)
	return r
}

func (r EngineRuntime) Validate() error {
	if !ValidID(string(r.BindingID)) || !ValidID(string(r.EpochID)) || r.Image != SpacetimeDBImage || r.Version != "2.11.0" || r.ManifestDigest != SpacetimeDBAMD64Manifest && r.ManifestDigest != SpacetimeDBARM64Manifest || !ValidCatalogHash(r.ImageID) || r.Runtime.UID != 1000 || r.Runtime.GID != 1000 || r.HostPort < 1024 || !ValidCatalogHash(r.UnitSHA256) {
		return ErrInvalid
	}
	if r.MemoryMB < 128 || r.MemoryMB > 65536 || r.PIDsLimit < 16 || r.PIDsLimit > 65536 || r.PagePoolMB < 16 || r.PagePoolMB >= r.MemoryMB || r.StopSeconds < 1 || r.StopSeconds > 60 || r.StartupSeconds < 1 || r.StartupSeconds > 300 || r.CopyBytes < 1 || r.CopyBytes > 1<<30 || r.CopyFiles < 1 || r.CopyFiles > 16384 || r.Module.Validate() != nil {
		return ErrInvalid
	}
	if err := validateEngineSecrets(r.Secrets); err != nil {
		return err
	}
	if r.Names == nil || len(r.Names) > 16 || !slices.IsSorted(r.Names) {
		return ErrInvalid
	}
	for i, name := range r.Names {
		if !namePattern.MatchString(name) || i > 0 && r.Names[i-1] == name {
			return ErrInvalid
		}
	}
	if r.State == EngineUnpublished {
		if r.Database != "" || r.Publisher != "" {
			return ErrInvalid
		}
		return nil
	}
	if r.State != EngineInitialized || !ValidCatalogHash(string(r.Database)) || !ValidCatalogHash(string(r.Publisher)) || len(r.Names) == 0 {
		return ErrInvalid
	}
	return nil
}

func validateEngineSecrets(s EngineSecrets) error {
	values := []EngineSecretVersion{s.Publisher, s.SigningPrivate, s.SigningPublic, s.AuthPolicy}
	seen := map[string]bool{}
	for _, secret := range values {
		if secret.Validate() != nil || seen[secret.Reference] {
			return ErrInvalid
		}
		seen[secret.Reference] = true
	}
	if s.Publisher.ExpiresAt.IsZero() {
		return ErrInvalid
	}
	return nil
}

func (r EngineRuntime) SecretsAdmit(now time.Time, budget time.Duration) bool {
	for _, secret := range []EngineSecretVersion{r.Secrets.Publisher, r.Secrets.SigningPrivate, r.Secrets.SigningPublic, r.Secrets.AuthPolicy} {
		if !secret.ExpiresAt.IsZero() && !now.Add(budget).Before(secret.ExpiresAt) {
			return false
		}
	}
	return budget >= 0
}

func (m *EngineModuleDefinition) UnmarshalJSON(raw []byte) error {
	f, err := engineRecordFields(raw, []string{"program_wire_hash", "wasm_sha256", "schema"}, nil)
	if err != nil {
		return err
	}
	if err = engineRecord(f["schema"], []string{"marker", "canonicalizer", "sha256"}, nil); err != nil {
		return err
	}
	type record EngineModuleDefinition
	var decoded record
	if err = json.Unmarshal(raw, &decoded); err != nil {
		return err
	}
	value := EngineModuleDefinition(decoded)
	if value.Validate() != nil {
		return ErrInvalid
	}
	*m = value
	return nil
}

func (r *EngineRuntime) UnmarshalJSON(raw []byte) error {
	if len(raw) > 65536 {
		return ErrInvalid
	}
	fields := []string{"binding_id", "epoch_id", "state", "image", "manifest_digest", "image_id", "version", "runtime", "host_port", "database_identity", "publisher_identity", "names", "module", "secrets", "memory_mb", "pids_limit", "page_pool_mb", "stop_seconds", "startup_seconds", "copy_bytes", "copy_files", "unit_sha256"}
	f, err := engineRecordFields(raw, fields, nil)
	if err != nil {
		return err
	}
	if err = engineRecord(f["runtime"], []string{"uid", "gid"}, nil); err != nil {
		return err
	}
	secrets, err := engineRecordFields(f["secrets"], []string{"publisher", "signing_private", "signing_public", "auth_policy"}, nil)
	if err != nil {
		return err
	}
	for _, secret := range secrets {
		if err = engineRecord(secret, []string{"reference", "version", "sha256", "expires_at"}, nil); err != nil {
			return err
		}
	}
	var names []json.RawMessage
	if json.Unmarshal(f["names"], &names) != nil || len(names) > 16 {
		return ErrInvalid
	}
	for _, name := range names {
		if _, err = strictjson.Value[string](name); err != nil {
			return ErrInvalid
		}
	}
	type record EngineRuntime
	var decoded record
	if err = json.Unmarshal(raw, &decoded); err != nil {
		return err
	}
	value := EngineRuntime(decoded)
	if value.Validate() != nil {
		return ErrInvalid
	}
	*r = value.Clone()
	return nil
}
