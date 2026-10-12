package plan

import (
	"encoding/json"
	"slices"
	"strings"

	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/spec"
	"github.com/ShaulLavo/brine/internal/strictjson"
	"github.com/ShaulLavo/brine/internal/target"
)

// ManagedServicePlan freezes expected managed ownership/configuration. Source
// proof and startup authority remain C's private, short-lived capabilities.
type ManagedServicePlan struct {
	Database       data.SpacetimeDBBinding `json:"database"`
	Archive        data.ArchiveBinding     `json:"archive"`
	EngineImage    Image                   `json:"engine_image"`
	EnginePort     target.Port             `json:"engine_port"`
	State          data.EngineDataState    `json:"state"`
	RuntimeSHA256  string                  `json:"runtime_sha256"`
	EngineUnitHash string                  `json:"engine_unit_hash"`
	Frontend       *ManagedFrontendPlan    `json:"frontend,omitempty"`
}

type ManagedFrontendPlan struct {
	Image   Image           `json:"image"`
	Port    target.Port     `json:"port"`
	Secrets []SecretBinding `json:"secrets"`
}

func (m ManagedServicePlan) Clone() ManagedServicePlan {
	m.EngineImage = cloneManagedImage(m.EngineImage)
	if m.Frontend != nil {
		frontend := *m.Frontend
		frontend.Image = cloneManagedImage(frontend.Image)
		frontend.Secrets = slices.Clone(frontend.Secrets)
		m.Frontend = &frontend
	}
	return m
}

func cloneManagedImage(image Image) Image {
	if image.ManifestDigest.Value != nil {
		value := *image.ManifestDigest.Value
		image.ManifestDigest.Value = &value
	}
	return image
}

func (m ManagedServicePlan) Valid() bool {
	if m.Archive.Validate(m.Database) != nil || m.EnginePort < 1024 || !managedEngineImageVerified(m.EngineImage) {
		return false
	}
	if m.State != data.EngineUnpublished && m.State != data.EngineInitialized {
		return false
	}
	if m.State == data.EngineInitialized && (!data.ValidCatalogHash(m.EngineUnitHash) || !data.ValidCatalogHash(m.RuntimeSHA256)) {
		return false
	}
	if m.State == data.EngineUnpublished && (m.RuntimeSHA256 != "" || m.EngineUnitHash != "") {
		return false
	}
	if m.RuntimeSHA256 != "" && !data.ValidCatalogHash(m.RuntimeSHA256) {
		return false
	}
	return m.Frontend == nil || m.Frontend.Valid(m.EnginePort)
}

func managedImageVerified(image Image) bool {
	return validHash(image.Digest) && image.validManifest() && image.ManifestDigest.Status == target.KnownStatus && image.ManifestDigest.Value != nil && image.Platform.OS == "linux" && (image.Platform.Arch == "amd64" || image.Platform.Arch == "arm64")
}

func managedEngineImageVerified(image Image) bool {
	_, digest, _ := strings.Cut(data.SpacetimeDBImage, "@")
	if !managedImageVerified(image) || image.Digest != digest {
		return false
	}
	manifest := data.SpacetimeDBAMD64Manifest
	if image.Platform.Arch == "arm64" {
		manifest = data.SpacetimeDBARM64Manifest
	}
	return *image.ManifestDigest.Value == manifest
}

func (f ManagedFrontendPlan) Valid(enginePort target.Port) bool {
	if f.Port < 1024 || f.Port == enginePort || !managedImageVerified(f.Image) || f.Secrets == nil || len(f.Secrets) > 128 {
		return false
	}
	seen := map[string]bool{}
	for _, secret := range f.Secrets {
		if !spec.ValidEnvironmentName(secret.Environment) || seen[secret.Environment] || secret.Reference == "" || secret.ID == "" || secret.VersionName == "" {
			return false
		}
		seen[secret.Environment] = true
	}
	return true
}

func (f *ManagedFrontendPlan) UnmarshalJSON(raw []byte) error {
	if len(raw) > 64<<10 || managedFrontendFields(raw) != nil {
		return strictjson.ErrObject
	}
	type record ManagedFrontendPlan
	value, err := strictjson.Value[record](raw)
	if err != nil || !ManagedFrontendPlan(value).Valid(0) {
		return strictjson.ErrObject
	}
	*f = ManagedFrontendPlan(value)
	return nil
}

func (m *ManagedServicePlan) UnmarshalJSON(raw []byte) error {
	if len(raw) > 64<<10 {
		return strictjson.ErrObject
	}
	fields, err := managedPlanFields(raw)
	if err != nil {
		return err
	}
	if value := fields["frontend"]; value != nil {
		if err := managedFrontendFields(value); err != nil {
			return err
		}
	}
	if err := managedImageFields(fields["engine_image"]); err != nil {
		return err
	}
	type record ManagedServicePlan
	value, err := strictjson.Value[record](raw)
	if err != nil || !ManagedServicePlan(value).Valid() {
		return strictjson.ErrObject
	}
	*m = ManagedServicePlan(value).Clone()
	return nil
}

func managedImageFields(raw json.RawMessage) error {
	fields, err := strictjson.Object(raw, "digest", "platform", "manifest_digest")
	if err != nil {
		return err
	}
	if _, err := strictjson.Object(fields["platform"], "os", "arch"); err != nil {
		return err
	}
	_, err = strictjson.Object(fields["manifest_digest"], "status", "value")
	return err
}

func managedFrontendFields(raw json.RawMessage) error {
	fields, err := strictjson.Object(raw, "image", "port", "secrets")
	if err != nil {
		return err
	}
	if err := managedImageFields(fields["image"]); err != nil {
		return err
	}
	secrets, err := strictjson.Value[[]json.RawMessage](fields["secrets"])
	if err != nil || len(secrets) > 128 {
		return strictjson.ErrObject
	}
	for _, secret := range secrets {
		if _, err := strictjson.Object(secret, "environment", "reference", "version_name", "id"); err != nil {
			return err
		}
	}
	return nil
}

func managedPlanFields(raw []byte) (map[string]json.RawMessage, error) {
	var presence map[string]json.RawMessage
	if json.Unmarshal(raw, &presence) != nil {
		return nil, strictjson.ErrObject
	}
	keys := []string{"database", "archive", "engine_image", "engine_port", "state", "runtime_sha256", "engine_unit_hash"}
	if _, present := presence["frontend"]; present {
		keys = append(keys, "frontend")
	}
	return strictjson.Object(raw, keys...)
}
