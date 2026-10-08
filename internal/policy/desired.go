package policy

import (
	"encoding/json"
	"slices"
	"strings"

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
	SchemaVersion int                 `json:"schema_version"`
	Name          spec.Name           `json:"name"`
	Image         spec.ImageReference `json:"image"`
	ContainerPort spec.Port           `json:"container_port"`
	Domains       []spec.Domain       `json:"domains"`
	Health        Health              `json:"health"`
	Resources     Resources           `json:"resources"`
	Environment   []Environment       `json:"environment"`
	Secrets       []Secret            `json:"secrets"`
	PolicyVersion string              `json:"policy_version"`
	PolicyHash    string              `json:"policy_hash"`
	AppPorts      PortRange           `json:"app_ports"`
}

func (d Desired) CanonicalBytes() ([]byte, error) {
	d.Domains = slices.Clone(d.Domains)
	slices.Sort(d.Domains)
	d.Environment = slices.Clone(d.Environment)
	slices.SortFunc(d.Environment, func(a, b Environment) int { return strings.Compare(a.Name, b.Name) })
	d.Secrets = slices.Clone(d.Secrets)
	slices.SortFunc(d.Secrets, func(a, b Secret) int { return strings.Compare(a.Name, b.Name) })
	return json.Marshal(d)
}

// Normalize requires a policy produced by Parse. No snapshot is needed here;
// observed ports, target identity and secret versions belong to the planner.
func Normalize(input spec.App, p Policy) (Desired, error) {
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
		if !slices.Contains(c.AllowedSecrets[string(app.Name)], string(ref)) {
			return Desired{}, refuse("policy.secret_denied", "secrets", "secret reference is not allowed for this app")
		}
	}
	d := Desired{SchemaVersion: SchemaVersion, Name: app.Name, Image: app.Image, ContainerPort: app.ContainerPort, Domains: app.Domains, Health: Health{Path: app.Health.Path, ExpectedStatus: app.Health.ExpectedStatus, StartupDeadlineSeconds: app.Health.StartupDeadlineSeconds, TimeoutSeconds: app.Health.TimeoutSeconds}, Resources: resources, Environment: []Environment{}, Secrets: []Secret{}, PolicyVersion: c.Version, PolicyHash: p.hash, AppPorts: *c.AppPorts}
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
