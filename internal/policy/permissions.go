package policy

import (
	"errors"
	"slices"
	"strings"

	"github.com/pelletier/go-toml/v2"
)

type PermissionKind string

const (
	AllowRegistry  PermissionKind = "allow_registry"
	RevokeRegistry PermissionKind = "revoke_registry"
	AllowDomain    PermissionKind = "allow_domain"
	RevokeDomain   PermissionKind = "revoke_domain"
)

type PermissionAction struct {
	Kind     PermissionKind `json:"kind"`
	Registry *Registry      `json:"registry,omitempty"`
	Domain   string         `json:"domain,omitempty"`
}

type Permissions struct {
	Registries []Registry `json:"registries"`
	Domains    []string   `json:"domains"`
}

func (p Policy) Permissions() Permissions {
	if p.config == nil {
		return Permissions{}
	}
	return Permissions{Registries: cloneRegistries(p.config.AllowedRegistries), Domains: slices.Clone(p.config.AllowedDomains)}
}

func cloneRegistries(input []Registry) []Registry {
	output := slices.Clone(input)
	for i := range output {
		output[i].RepositoryPrefixes = slices.Clone(output[i].RepositoryPrefixes)
	}
	return output
}

func (p Policy) EditPermissions(actions []PermissionAction) (Policy, []byte, error) {
	if p.config == nil || len(actions) == 0 || len(actions) > 128 {
		return Policy{}, nil, errors.New("invalid permission changes")
	}
	next := *p.config
	next.AllowedRegistries = cloneRegistries(next.AllowedRegistries)
	next.AllowedDomains = slices.Clone(next.AllowedDomains)
	seen := map[string]bool{}
	for _, action := range actions {
		key, err := applyPermission(&next, action)
		if err != nil || seen[key] {
			return Policy{}, nil, errors.New("invalid or repeated permission change")
		}
		seen[key] = true
	}
	raw, err := toml.Marshal(next)
	if err != nil {
		return Policy{}, nil, err
	}
	parsed, err := Parse(raw)
	if err != nil {
		return Policy{}, nil, err
	}
	raw, err = toml.Marshal(parsed.config)
	return parsed, raw, err
}

func applyPermission(next *document, action PermissionAction) (string, error) {
	switch action.Kind {
	case AllowDomain, RevokeDomain:
		if action.Registry != nil || action.Domain == "" || strings.Contains(action.Domain, "*") {
			return "", errors.New("expected an exact domain")
		}
		domain := strings.ToLower(action.Domain)
		if !cleanASCII(domain) || !validDomain(domain) {
			return "", errors.New("invalid exact domain")
		}
		next.AllowedDomains = slices.DeleteFunc(next.AllowedDomains, func(value string) bool { return value == domain })
		if action.Kind == AllowDomain {
			next.AllowedDomains = append(next.AllowedDomains, domain)
		}
		return "domain:" + domain, nil
	case AllowRegistry, RevokeRegistry:
		if action.Registry == nil || action.Domain != "" || len(action.Registry.RepositoryPrefixes) != 1 || action.Registry.RepositoryPrefixes[0] == "" {
			return "", errors.New("expected one registry repository prefix")
		}
		return editRegistry(next, action)
	default:
		return "", errors.New("unknown permission action")
	}
}

func editRegistry(next *document, action PermissionAction) (string, error) {
	r := *action.Registry
	r.Host = strings.ToLower(r.Host)
	prefix := r.RepositoryPrefixes[0]
	if !cleanASCII(r.Host) || strings.ContainsAny(r.Host, "/@") || !validImage(r.Host, prefix) || strings.ContainsAny(prefix, ":@") {
		return "", errors.New("invalid registry repository prefix")
	}
	index := slices.IndexFunc(next.AllowedRegistries, func(value Registry) bool { return value.Host == r.Host })
	if index < 0 && action.Kind == RevokeRegistry {
		return "registry:" + r.Host + "/" + prefix, nil
	}
	if index < 0 {
		next.AllowedRegistries = append(next.AllowedRegistries, Registry{Host: r.Host})
		index = len(next.AllowedRegistries) - 1
	}
	values := slices.DeleteFunc(next.AllowedRegistries[index].RepositoryPrefixes, func(value string) bool { return value == prefix })
	if action.Kind == AllowRegistry {
		values = append(values, prefix)
	}
	slices.Sort(values)
	next.AllowedRegistries[index].RepositoryPrefixes = values
	if len(values) == 0 {
		next.AllowedRegistries = slices.Delete(next.AllowedRegistries, index, index+1)
	}
	return "registry:" + r.Host + "/" + prefix, nil
}
