package policy

import (
	"encoding/json"
	"slices"
	"strings"

	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/spec"
)

type Environment struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}
type Secret struct {
	Name      string               `json:"name"`
	Reference spec.SecretReference `json:"reference"`
}
type Health struct {
	Path                   spec.HealthPath `json:"path"`
	ExpectedStatus         int             `json:"expected_status"`
	StartupDeadlineSeconds int             `json:"startup_deadline_seconds"`
	TimeoutSeconds         int             `json:"timeout_seconds"`
}

// Desired is hash material for P01-03, not a plan or a host-port allocation.
// Environment includes literal app settings, never resolved secret values.
// Treat the returned value as immutable. CanonicalBytes also sorts a defensive
// copy so serialization stays deterministic if a caller reorders collections.
type Desired struct {
	Runtime              *data.RuntimeIdentity      `json:"runtime,omitempty"`
	Databases            []data.Database            `json:"databases,omitempty"`
	SchemaCompatibility  []data.SchemaCompatibility `json:"schema_compatibility,omitempty"`
	SchemaDefinitions    []data.SchemaDefinition    `json:"schema_definitions,omitempty"`
	SchemaVersion        int                        `json:"schema_version"`
	Name                 spec.Name                  `json:"name"`
	Image                spec.ImageReference        `json:"image"`
	ContainerPort        spec.Port                  `json:"container_port"`
	Domains              []spec.Domain              `json:"domains"`
	Health               Health                     `json:"health"`
	Resources            Resources                  `json:"resources"`
	Environment          []Environment              `json:"environment"`
	Secrets              []Secret                   `json:"secrets"`
	PolicyVersion        string                     `json:"policy_version"`
	PolicyHash           string                     `json:"policy_hash"`
	AppPorts             PortRange                  `json:"app_ports"`
	MinimumFreeDiskBytes uint64                     `json:"minimum_free_disk_bytes"`
}

func (d Desired) CanonicalBytes() ([]byte, error) {
	if d.MinimumFreeDiskBytes == 0 {
		d.MinimumFreeDiskBytes = DefaultMinimumFreeDiskBytes
	}
	d.Domains = slices.Clone(d.Domains)
	slices.Sort(d.Domains)
	d.Environment = slices.Clone(d.Environment)
	slices.SortFunc(d.Environment, func(a, b Environment) int { return strings.Compare(a.Name, b.Name) })
	d.Secrets = slices.Clone(d.Secrets)
	slices.SortFunc(d.Secrets, func(a, b Secret) int { return strings.Compare(a.Name, b.Name) })
	d.Databases = slices.Clone(d.Databases)
	slices.SortFunc(d.Databases, func(a, b data.Database) int { return strings.Compare(string(a.Name), string(b.Name)) })
	d.SchemaCompatibility = slices.Clone(d.SchemaCompatibility)
	for i := range d.SchemaCompatibility {
		d.SchemaCompatibility[i].Accepts = slices.Clone(d.SchemaCompatibility[i].Accepts)
		slices.Sort(d.SchemaCompatibility[i].Accepts)
	}
	slices.SortFunc(d.SchemaCompatibility, func(a, b data.SchemaCompatibility) int {
		return strings.Compare(string(a.Database), string(b.Database))
	})
	d.SchemaDefinitions = slices.Clone(d.SchemaDefinitions)
	slices.SortFunc(d.SchemaDefinitions, func(a, b data.SchemaDefinition) int {
		if c := strings.Compare(string(a.Database), string(b.Database)); c != 0 {
			return c
		}
		return strings.Compare(a.Marker, b.Marker)
	})
	return json.Marshal(d)
}

// Normalize requires a policy produced by Parse. No snapshot is needed here;
// observed ports, target identity and secret versions belong to the planner.
func Normalize(input spec.App, p Policy) (Desired, error) {
	if input.Runtime != nil || len(input.Databases) != 0 || len(input.SchemaCompatibility) != 0 || len(input.SchemaDefinitions) != 0 {
		return Desired{}, refuse("policy.persistence_unavailable", "databases", "persistent admission requires verified data facts")
	}
	if p.config == nil {
		return Desired{}, refuse("policy.required", "$", "explicit operator policy is required")
	}
	// Branded primitives and App are public Go values. Reuse the spec boundary to
	// refuse forged or mutated values rather than trusting a cast as validation.
	domains := make([]string, len(input.Domains))
	for i, d := range input.Domains {
		domains[i] = string(d)
	}
	secrets := map[string]string{}
	for name, ref := range input.Secrets {
		secrets[name] = string(ref)
	}
	raw := map[string]any{"schema_version": input.SchemaVersion, "name": string(input.Name), "image": string(input.Image), "container_port": int(input.ContainerPort), "domains": domains, "environment": input.Environment, "secrets": secrets}
	raw["health"] = map[string]any{
		"path":                     string(input.Health.Path),
		"expected_status":          input.Health.ExpectedStatus,
		"startup_deadline_seconds": input.Health.StartupDeadlineSeconds,
		"timeout_seconds":          input.Health.TimeoutSeconds,
	}
	if input.Resources != nil {
		raw["resources"] = map[string]int{"memory_mb": input.Resources.MemoryMB, "pids_limit": input.Resources.PIDsLimit}
	}
	app, e := parseMap(raw)
	if e != nil {
		return Desired{}, refuse("policy.invalid_spec", "$", "app configuration failed the strict spec boundary")
	}
	c := p.config
	ref := strings.SplitN(string(app.Image), "@", 2)[0]
	host, repo, _ := strings.Cut(ref, "/")
	repo = strings.SplitN(repo, ":", 2)[0]
	allowed := false
	for _, registry := range c.AllowedRegistries {
		if registry.Host != host {
			continue
		}
		for _, prefix := range registry.RepositoryPrefixes {
			if repo == prefix || strings.HasPrefix(repo, prefix+"/") {
				allowed = true
			}
		}
	}
	if !allowed {
		return Desired{}, refuse("policy.registry_denied", "image", "image registry or repository is not allowed")
	}
	for _, domain := range app.Domains {
		allowed = false
		for _, rule := range c.AllowedDomains {
			if strings.HasPrefix(rule, "*.") {
				if strings.HasSuffix(string(domain), rule[1:]) {
					allowed = true
				}
			} else if string(domain) == rule {
				allowed = true
			}
		}
		if !allowed {
			return Desired{}, refuse("policy.domain_denied", "domains", "domain is not allowed")
		}
	}
	resources := c.Resources
	if app.Resources != nil {
		resources = Resources{MemoryMB: app.Resources.MemoryMB, PIDsLimit: app.Resources.PIDsLimit}
	}
	if resources.MemoryMB > c.Resources.MemoryMB || resources.PIDsLimit > c.Resources.PIDsLimit {
		return Desired{}, refuse("policy.resources_denied", "resources", "requested resources exceed operator ceilings")
	}
	for _, ref := range app.Secrets {
		if e := p.CheckSecret(app.Name, ref); e != nil {
			return Desired{}, e
		}
	}
	d := Desired{SchemaVersion: SchemaVersion, Name: app.Name, Image: app.Image, ContainerPort: app.ContainerPort, Domains: app.Domains, Health: Health{Path: app.Health.Path, ExpectedStatus: app.Health.ExpectedStatus, StartupDeadlineSeconds: app.Health.StartupDeadlineSeconds, TimeoutSeconds: app.Health.TimeoutSeconds}, Resources: resources, Environment: []Environment{}, Secrets: []Secret{}, PolicyVersion: c.Version, PolicyHash: p.hash, AppPorts: *c.AppPorts, MinimumFreeDiskBytes: *c.MinimumFreeDiskBytes}
	slices.Sort(d.Domains)
	for name, value := range app.Environment {
		d.Environment = append(d.Environment, Environment{Name: name, Value: value})
	}
	slices.SortFunc(d.Environment, func(a, b Environment) int { return strings.Compare(a.Name, b.Name) })
	for name, ref := range app.Secrets {
		d.Secrets = append(d.Secrets, Secret{Name: name, Reference: ref})
	}
	slices.SortFunc(d.Secrets, func(a, b Secret) int { return strings.Compare(a.Name, b.Name) })
	return d, nil
}

// CheckSecret is the same app-scoped reference gate used by Normalize. It also
// works before an app has a release, so first deployment can bind a secret.
func (p Policy) CheckSecret(app spec.Name, ref spec.SecretReference) error {
	if p.config == nil {
		return refuse("policy.required", "$", "explicit operator policy is required")
	}
	if !referencePattern.MatchString(string(ref)) || !slices.Contains(p.config.AllowedSecrets[string(app)], string(ref)) {
		return refuse("policy.secret_denied", "secrets", "secret reference is not allowed for this app")
	}
	return nil
}

// Stateless is affirmative only when no persistence declaration is present.
func (d Desired) Stateless() bool {
	return d.SchemaVersion == 1 && d.Runtime == nil && len(d.Databases) == 0 && len(d.SchemaCompatibility) == 0 && len(d.SchemaDefinitions) == 0
}

// App reconstructs a detached spec input. It grants no policy authorization.
func (d Desired) App() spec.App {
	a := spec.App{SchemaVersion: d.SchemaVersion, Name: d.Name, Image: d.Image, ContainerPort: d.ContainerPort, Domains: slices.Clone(d.Domains), Health: spec.Health{Path: d.Health.Path, ExpectedStatus: d.Health.ExpectedStatus, StartupDeadlineSeconds: d.Health.StartupDeadlineSeconds, TimeoutSeconds: d.Health.TimeoutSeconds}, Resources: &spec.Resources{MemoryMB: d.Resources.MemoryMB, PIDsLimit: d.Resources.PIDsLimit}, Environment: map[string]string{}, Secrets: map[string]spec.SecretReference{}, Databases: slices.Clone(d.Databases), SchemaCompatibility: slices.Clone(d.SchemaCompatibility), SchemaDefinitions: slices.Clone(d.SchemaDefinitions)}
	if d.Runtime != nil {
		r := *d.Runtime
		a.Runtime = &r
	}
	for i := range a.SchemaCompatibility {
		a.SchemaCompatibility[i].Accepts = slices.Clone(a.SchemaCompatibility[i].Accepts)
	}
	for _, v := range d.Environment {
		a.Environment[v.Name] = v.Value
	}
	for _, v := range d.Secrets {
		a.Secrets[v.Name] = v.Reference
	}
	return a
}
