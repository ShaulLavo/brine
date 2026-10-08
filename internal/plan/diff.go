package plan

import (
	"reflect"
	"slices"
	"strings"

	"github.com/ShaulLavo/brine/internal/policy"
	"github.com/ShaulLavo/brine/internal/spec"
	"github.com/ShaulLavo/brine/internal/target"
)

// ValueChange has a nil From for creation and a nil To for removal.
// Unchanged fields are absent from ConfigurationDiff.
type ValueChange[T any] struct {
	From *T `json:"from"`
	To   *T `json:"to"`
}

type SetChange[T any] struct {
	Added   []T `json:"added"`
	Removed []T `json:"removed"`
}

type EnvironmentChange struct {
	Added   []string `json:"added"`
	Removed []string `json:"removed"`
	Changed []string `json:"changed"`
}

type SecretVersion struct {
	Reference   spec.SecretReference `json:"reference"`
	VersionName string               `json:"version_name"`
}

type SecretChange struct {
	Environment string         `json:"environment"`
	From        *SecretVersion `json:"from"`
	To          *SecretVersion `json:"to"`
}

// ConfigurationDiff records only changed settings. Environment values are
// compared transiently and never retained here or in the returned plan.
type ConfigurationDiff struct {
	Image         *ValueChange[target.Image]     `json:"image,omitempty"`
	Domains       *SetChange[spec.Domain]        `json:"domains,omitempty"`
	HostPort      *ValueChange[target.Port]      `json:"host_port,omitempty"`
	ContainerPort *ValueChange[spec.Port]        `json:"container_port,omitempty"`
	Resources     *ValueChange[policy.Resources] `json:"resources,omitempty"`
	Health        *ValueChange[policy.Health]    `json:"health,omitempty"`
	Environment   *EnvironmentChange             `json:"environment,omitempty"`
	Secrets       []SecretChange                 `json:"secrets"`
}

func valueChange[T comparable](old *T, next T) *ValueChange[T] {
	if old != nil && *old == next {
		return nil
	}
	return &ValueChange[T]{From: old, To: &next}
}

func configurationDiff(next policy.Desired, image target.Image, port target.Port, secrets []SecretBinding, previous *CurrentRelease) *ConfigurationDiff {
	d := &ConfigurationDiff{Secrets: []SecretChange{}}
	old := policy.Desired{}
	oldSecrets := []SecretBinding{}
	if previous == nil {
		d.Image = valueChange[target.Image](nil, image)
		d.HostPort = valueChange[target.Port](nil, port)
		d.ContainerPort = valueChange[spec.Port](nil, next.ContainerPort)
		d.Resources = valueChange[policy.Resources](nil, next.Resources)
		d.Health = valueChange[policy.Health](nil, next.Health)
	} else {
		old = previous.Desired
		oldSecrets = previous.Secrets
		d.Image = valueChange(&previous.Image, image)
		d.HostPort = valueChange(&previous.HostPort, port)
		d.ContainerPort = valueChange(&old.ContainerPort, next.ContainerPort)
		d.Resources = valueChange(&old.Resources, next.Resources)
		d.Health = valueChange(&old.Health, next.Health)
	}
	domains := &SetChange[spec.Domain]{Added: []spec.Domain{}, Removed: []spec.Domain{}}
	for _, domain := range next.Domains {
		if !slices.Contains(old.Domains, domain) {
			domains.Added = append(domains.Added, domain)
		}
	}
	for _, domain := range old.Domains {
		if !slices.Contains(next.Domains, domain) {
			domains.Removed = append(domains.Removed, domain)
		}
	}
	slices.Sort(domains.Added)
	slices.Sort(domains.Removed)
	if len(domains.Added)+len(domains.Removed) > 0 {
		d.Domains = domains
	}
	env := &EnvironmentChange{Added: []string{}, Removed: []string{}, Changed: []string{}}
	before := map[string]string{}
	after := map[string]string{}
	for _, e := range old.Environment {
		before[e.Name] = e.Value
	}
	for _, e := range next.Environment {
		after[e.Name] = e.Value
	}
	for key, value := range after {
		if prior, ok := before[key]; !ok {
			env.Added = append(env.Added, key)
		} else if prior != value {
			env.Changed = append(env.Changed, key)
		}
	}
	for key := range before {
		if _, ok := after[key]; !ok {
			env.Removed = append(env.Removed, key)
		}
	}
	slices.Sort(env.Added)
	slices.Sort(env.Removed)
	slices.Sort(env.Changed)
	if len(env.Added)+len(env.Removed)+len(env.Changed) > 0 {
		d.Environment = env
	}
	prior := map[string]SecretVersion{}
	desired := map[string]SecretVersion{}
	for _, s := range oldSecrets {
		prior[s.Environment] = SecretVersion{s.Reference, s.VersionName}
	}
	for _, s := range secrets {
		desired[s.Environment] = SecretVersion{s.Reference, s.VersionName}
	}
	for key, to := range desired {
		from, ok := prior[key]
		if ok && from == to {
			continue
		}
		c := SecretChange{Environment: key, To: &to}
		if ok {
			c.From = &from
		}
		d.Secrets = append(d.Secrets, c)
	}
	for key, from := range prior {
		if _, ok := desired[key]; !ok {
			d.Secrets = append(d.Secrets, SecretChange{Environment: key, From: &from})
		}
	}
	slices.SortFunc(d.Secrets, func(a, b SecretChange) int { return strings.Compare(a.Environment, b.Environment) })
	if reflect.DeepEqual(*d, ConfigurationDiff{Secrets: []SecretChange{}}) {
		return nil
	}
	return d
}
