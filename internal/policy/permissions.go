package policy

import (
	"bytes"
	"errors"
	"slices"
	"strings"

	"github.com/ShaulLavo/brine/internal/cdn"
	"github.com/pelletier/go-toml/v2"
)

type PermissionKind string

const (
	AllowUDPGrant    PermissionKind = "allow_udp_grant"
	RevokeUDPGrant   PermissionKind = "revoke_udp_grant"
	AllowRegistry    PermissionKind = "allow_registry"
	RevokeRegistry   PermissionKind = "revoke_registry"
	AllowDomain      PermissionKind = "allow_domain"
	RevokeDomain     PermissionKind = "revoke_domain"
	AllowCDNBinding  PermissionKind = "allow_cdn_binding"
	RevokeCDNBinding PermissionKind = "revoke_cdn_binding"
	AllowSecret      PermissionKind = "allow_secret"
	RevokeSecret     PermissionKind = "revoke_secret"
)

type PermissionAction struct {
	UDPGrant   *UDPGrant         `json:"udp_grant,omitempty"`
	UDPApp     string            `json:"udp_app,omitempty"`
	Kind       PermissionKind    `json:"kind"`
	Registry   *Registry         `json:"registry,omitempty"`
	Domain     string            `json:"domain,omitempty"`
	CDNBinding *cdn.Binding      `json:"cdn_binding,omitempty"`
	Secret     *SecretPermission `json:"secret,omitempty"`
}

type Permissions struct {
	UDPGrants      []UDPGrant          `json:"udp_grants"`
	Registries     []Registry          `json:"registries"`
	Domains        []string            `json:"domains"`
	CDNBindings    []cdn.Binding       `json:"cdn_bindings"`
	AllowedSecrets map[string][]string `json:"allowed_secrets"`
}

func (p Policy) Permissions() Permissions {
	if p.config == nil {
		return Permissions{}
	}
	return Permissions{UDPGrants: append([]UDPGrant{}, p.config.UDPGrants...), Registries: cloneRegistries(p.config.AllowedRegistries), Domains: slices.Clone(p.config.AllowedDomains), CDNBindings: cloneCDNBindings(p.config.CDNBindings), AllowedSecrets: cloneAllowedSecrets(p.config.AllowedSecrets)}
}

func cloneCDNBindings(input []cdn.Binding) []cdn.Binding {
	output := make([]cdn.Binding, len(input))
	for i, binding := range input {
		output[i] = cloneCDNBinding(binding)
	}
	return output
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
	next.CDNBindings = cloneCDNBindings(next.CDNBindings)
	next.UDPGrants = slices.Clone(next.UDPGrants)
	next.AllowedSecrets = cloneAllowedSecrets(next.AllowedSecrets)
	if err := applyPermissions(&next, actions); err != nil {
		return Policy{}, nil, err
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

// ValidatePermissionActions checks the closed action union and exact change identities.
func ValidatePermissionActions(actions []PermissionAction) error {
	return applyPermissions(&document{}, actions)
}

func applyPermissions(next *document, actions []PermissionAction) error {
	if len(actions) == 0 || len(actions) > 128 {
		return errors.New("invalid permission changes")
	}
	seen := map[string]bool{}
	for _, action := range actions {
		key, err := applyPermission(next, action)
		if err != nil || seen[key] {
			return errors.New("invalid or repeated permission change")
		}
		seen[key] = true
	}
	return nil
}

func applyPermission(next *document, action PermissionAction) (string, error) {
	if action.Kind == AllowUDPGrant || action.Kind == RevokeUDPGrant {
		return editUDPGrant(next, action)
	}
	if action.UDPGrant != nil || action.UDPApp != "" {
		return "", errors.New("unexpected UDP permission payload")
	}
	switch action.Kind {
	case AllowDomain, RevokeDomain:
		if action.Registry != nil || action.CDNBinding != nil || action.Secret != nil || action.Domain == "" || strings.Contains(action.Domain, "*") {
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
		if action.Registry == nil || action.CDNBinding != nil || action.Secret != nil || action.Domain != "" || len(action.Registry.RepositoryPrefixes) != 1 || action.Registry.RepositoryPrefixes[0] == "" {
			return "", errors.New("expected one registry repository prefix")
		}
		return editRegistry(next, action)
	case AllowCDNBinding, RevokeCDNBinding:
		if action.CDNBinding == nil || action.Registry != nil || action.Secret != nil || action.Domain != "" {
			return "", errors.New("expected one exact CDN binding")
		}
		return editCDNBinding(next, action)
	case AllowSecret, RevokeSecret:
		if action.Secret == nil || action.Registry != nil || action.CDNBinding != nil || action.Domain != "" || !action.Secret.Valid() {
			return "", errors.New("expected one exact app secret reference")
		}
		return editSecretPermission(next, action), nil
	default:
		return "", errors.New("unknown permission action")
	}
}

func editCDNBinding(next *document, action PermissionAction) (string, error) {
	canonical, err := action.CDNBinding.CanonicalBytes()
	if err != nil {
		return "", errors.New("invalid CDN binding")
	}
	binding, err := cdn.ParseBinding(canonical)
	if err != nil {
		return "", errors.New("invalid CDN binding")
	}
	index := slices.IndexFunc(next.CDNBindings, func(value cdn.Binding) bool { return value.Reference == binding.Reference })
	if action.Kind == AllowCDNBinding {
		next.CDNBindings = slices.DeleteFunc(next.CDNBindings, func(value cdn.Binding) bool { return value.Reference == binding.Reference })
		next.CDNBindings = append(next.CDNBindings, binding)
		return "cdn_binding:" + binding.Reference, nil
	}
	if index < 0 {
		return "cdn_binding:" + binding.Reference, nil
	}
	current, err := next.CDNBindings[index].CanonicalBytes()
	if err != nil || !bytes.Equal(current, canonical) {
		return "", errors.New("CDN revocation must match the exact binding")
	}
	next.CDNBindings = slices.Delete(next.CDNBindings, index, index+1)
	return "cdn_binding:" + binding.Reference, nil
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

func editUDPGrant(next *document, action PermissionAction) (string, error) {
	if action.Registry != nil || action.Domain != "" || action.CDNBinding != nil || action.Secret != nil {
		return "", errors.New("mixed UDP permission")
	}
	app := action.UDPApp
	if action.Kind == AllowUDPGrant {
		ports := PortRange{}
		if next.AppPorts != nil {
			ports = *next.AppPorts
		}
		if action.UDPGrant == nil || action.UDPApp != "" || action.UDPGrant.Validate(ports) != nil {
			return "", errors.New("invalid UDP grant")
		}
		app = action.UDPGrant.App
	} else if action.UDPGrant != nil || !udpAppPattern.MatchString(app) {
		return "", errors.New("invalid UDP revocation")
	}
	next.UDPGrants = slices.DeleteFunc(next.UDPGrants, func(g UDPGrant) bool { return g.App == app })
	if action.Kind == AllowUDPGrant {
		next.UDPGrants = append(next.UDPGrants, *action.UDPGrant)
	}
	return "udp:" + app, nil
}
