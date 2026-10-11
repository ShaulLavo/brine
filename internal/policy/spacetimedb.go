package policy

import (
	"slices"
	"strings"

	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/spec"
)

type SpacetimePolicy struct {
	LicenseUse                  string `json:"license_use" toml:"license_use"`
	LicenseAuthority            string `json:"license_authority" toml:"license_authority"`
	CaptureIntervalSeconds      int    `json:"capture_interval_seconds" toml:"capture_interval_seconds"`
	VerificationIntervalSeconds int    `json:"verification_interval_seconds" toml:"verification_interval_seconds"`
	MaxRemoteLossSeconds        int    `json:"max_remote_loss_seconds" toml:"max_remote_loss_seconds"`
	MaxMaintenanceSeconds       int    `json:"max_maintenance_seconds" toml:"max_maintenance_seconds"`
}

func (p SpacetimePolicy) Valid() bool {
	if p.LicenseUse != "fixture" && p.LicenseUse != "single_production_instance" && p.LicenseUse != "alternative" {
		return false
	}
	if !revisionPattern.MatchString(p.LicenseAuthority) {
		return false
	}
	for _, v := range []int{p.CaptureIntervalSeconds, p.VerificationIntervalSeconds, p.MaxRemoteLossSeconds, p.MaxMaintenanceSeconds} {
		if v < 1 || v > 365*24*3600 {
			return false
		}
	}
	return true
}

func normalizeManaged(a spec.App, p Policy) (Desired, error) {
	if spec.ValidateManagedApp(a) != nil {
		return Desired{}, refuse("policy.invalid_spec", "databases", "invalid managed database declaration")
	}
	engine := a.SpacetimeDB.Clone()
	c := p.config
	if c.SpacetimeDB == nil || !c.SpacetimeDB.Valid() {
		return Desired{}, refuse("policy.spacetimedb_review_required", "spacetimedb", "explicit license and backup review facts are required")
	}
	if !managedRegistryAllowed(engine.Image, c.AllowedRegistries) {
		return Desired{}, refuse("policy.registry_denied", "databases.spacetimedb.image", "image registry or repository is not allowed")
	}
	if !slices.Contains(c.PersistentRoots, string(engine.PersistentRoot)) {
		return Desired{}, refuse("policy.persistent_root_denied", "databases.persistent_root", "persistent root is not allowed")
	}
	destination, ok := p.BackupDestination(engine.BackupDestination)
	if !ok {
		return Desired{}, refuse("policy.backup_destination_unknown", "databases.backup_destination", "backup destination is not allowed")
	}
	for _, ref := range engine.References() {
		if err := p.CheckSecret(a.Name, ref); err != nil {
			return Desired{}, err
		}
	}
	for _, domain := range a.Domains {
		if !managedDomainAllowed(domain, c.AllowedDomains) {
			return Desired{}, refuse("policy.domain_denied", "domains", "domain is not allowed")
		}
	}
	resources := Resources{}
	if a.HasAppContainer() {
		if !managedRegistryAllowed(a.Image, c.AllowedRegistries) {
			return Desired{}, refuse("policy.registry_denied", "image", "image registry or repository is not allowed")
		}
		resources = c.Resources
		if a.Resources != nil {
			resources = Resources{MemoryMB: a.Resources.MemoryMB, PIDsLimit: a.Resources.PIDsLimit}
		}
	}
	if resources.MemoryMB > c.Resources.MemoryMB-engine.Resources.MemoryMB || resources.PIDsLimit > c.Resources.PIDsLimit-engine.Resources.PIDsLimit {
		return Desired{}, refuse("policy.resources_denied", "resources", "combined app and engine resources exceed operator ceilings")
	}
	review := *c.SpacetimeDB
	d := Desired{SpacetimeDB: engine, SpacetimePolicy: &review, SchemaVersion: SchemaVersion, Name: a.Name, Image: a.Image, ContainerPort: a.ContainerPort, Domains: slices.Clone(a.Domains), Health: Health{Path: a.Health.Path, ExpectedStatus: a.Health.ExpectedStatus, StartupDeadlineSeconds: a.Health.StartupDeadlineSeconds, TimeoutSeconds: a.Health.TimeoutSeconds}, Resources: resources, Environment: []Environment{}, Secrets: []Secret{}, PolicyVersion: c.Version, PolicyHash: p.hash, AppPorts: *c.AppPorts, MinimumFreeDiskBytes: *c.MinimumFreeDiskBytes, BackupDestinations: []data.Destination{destination}, SchemaCompatibility: slices.Clone(a.SchemaCompatibility)}
	for _, root := range c.PersistentRoots {
		d.PersistentRoots = append(d.PersistentRoots, data.PersistentRoot(root))
	}
	if retention, ok := p.BackupRetention(destination.Reference); ok {
		d.BackupRetention = []data.RetentionEvidence{retention}
	}
	for i := range d.SchemaCompatibility {
		d.SchemaCompatibility[i].Accepts = slices.Clone(a.SchemaCompatibility[i].Accepts)
	}
	for name, value := range a.Environment {
		d.Environment = append(d.Environment, Environment{Name: name, Value: value})
	}
	for name, ref := range a.Secrets {
		if slices.Contains(engine.References(), ref) {
			return Desired{}, refuse("policy.secret_purpose_collision", "secrets", "engine credentials cannot be delivered to an application container")
		}
		if err := p.CheckSecret(a.Name, ref); err != nil {
			return Desired{}, err
		}
		d.Secrets = append(d.Secrets, Secret{Name: name, Reference: ref})
	}
	slices.Sort(d.Domains)
	slices.SortFunc(d.Environment, func(a, b Environment) int { return strings.Compare(a.Name, b.Name) })
	slices.SortFunc(d.Secrets, func(a, b Secret) int { return strings.Compare(a.Name, b.Name) })
	d.DatabaseEnvironment = d.DatabaseEnvironmentValues()
	return d, nil
}

func managedRegistryAllowed(image spec.ImageReference, allowed []Registry) bool {
	ref := strings.SplitN(string(image), "@", 2)[0]
	host, repo, _ := strings.Cut(ref, "/")
	repo = strings.SplitN(repo, ":", 2)[0]
	for _, rule := range allowed {
		if rule.Host != host {
			continue
		}
		if managedRepositoryAllowed(repo, rule.RepositoryPrefixes) {
			return true
		}
	}
	return false
}

func managedRepositoryAllowed(repo string, prefixes []string) bool {
	for _, prefix := range prefixes {
		if repo == prefix || strings.HasPrefix(repo, prefix+"/") {
			return true
		}
	}
	return false
}

func managedDomainAllowed(domain spec.Domain, allowed []string) bool {
	for _, rule := range allowed {
		if string(domain) == rule || strings.HasPrefix(rule, "*.") && strings.HasSuffix(string(domain), rule[1:]) {
			return true
		}
	}
	return false
}

func managedApp(d Desired) spec.App {
	a := spec.App{SchemaVersion: d.SchemaVersion, Name: d.Name, Image: d.Image, ContainerPort: d.ContainerPort, Domains: slices.Clone(d.Domains), Health: spec.Health{Path: d.Health.Path, ExpectedStatus: d.Health.ExpectedStatus, StartupDeadlineSeconds: d.Health.StartupDeadlineSeconds, TimeoutSeconds: d.Health.TimeoutSeconds}, Environment: map[string]string{}, Secrets: map[string]spec.SecretReference{}, SchemaCompatibility: slices.Clone(d.SchemaCompatibility)}
	if d.SpacetimeDB != nil {
		a.SpacetimeDB = d.SpacetimeDB.Clone()
	}
	if d.Image != "" {
		a.Resources = &spec.Resources{MemoryMB: d.Resources.MemoryMB, PIDsLimit: d.Resources.PIDsLimit}
	}
	for i := range a.SchemaCompatibility {
		a.SchemaCompatibility[i].Accepts = slices.Clone(d.SchemaCompatibility[i].Accepts)
	}
	for _, item := range d.Environment {
		a.Environment[item.Name] = item.Value
	}
	for _, item := range d.Secrets {
		a.Secrets[item.Name] = item.Reference
	}
	return a
}

func ValidateManagedDesired(d Desired) error {
	if d.SpacetimeDB == nil || d.SpacetimePolicy == nil || !d.SpacetimePolicy.Valid() || d.PolicyVersion == "" || !data.ValidCatalogHash(strings.TrimPrefix(d.PolicyHash, "sha256:")) || !strings.HasPrefix(d.PolicyHash, "sha256:") || d.AppPorts.Min < 1024 || d.AppPorts.Max < d.AppPorts.Min {
		return data.ErrInvalid
	}
	if len(d.Databases) != 0 || d.Runtime != nil || d.Backup != nil || len(d.RestoreInvariants) != 0 || len(d.SchemaDefinitions) != 0 || len(d.SchemaArtifacts) != 0 || !slices.Contains(d.PersistentRoots, d.SpacetimeDB.PersistentRoot) || len(d.BackupDestinations) != 1 || d.BackupDestinations[0].Reference != d.SpacetimeDB.BackupDestination || d.BackupDestinations[0].Validate() != nil {
		return data.ErrInvalid
	}
	if len(d.Environment) != len(managedApp(d).Environment) || len(d.Secrets) != len(managedApp(d).Secrets) {
		return data.ErrInvalid
	}
	if !managedApp(d).HasAppContainer() && d.Resources != (Resources{}) {
		return data.ErrInvalid
	}
	for _, secret := range d.Secrets {
		if slices.Contains(d.SpacetimeDB.References(), secret.Reference) {
			return data.ErrInvalid
		}
	}
	return spec.ValidateManagedApp(managedApp(d))
}
