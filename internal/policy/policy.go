package policy

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"path"
	"regexp"
	"slices"
	"strings"

	"github.com/ShaulLavo/brine/internal/spec"
	"github.com/pelletier/go-toml/v2"
)

const SchemaVersion = 1

const DefaultMinimumFreeDiskBytes uint64 = 1 << 30

// Refusal diagnostics contain only package-owned text, including for malformed
// input. Category is the machine contract's refused-by-policy exit category.
type Refusal struct {
	Code    string
	Field   string
	Message string
}

func (r *Refusal) Error() string { return r.Code + " at " + r.Field + ": " + r.Message }
func (r *Refusal) Category() int { return 4 }
func refuse(code, field, message string) error {
	return &Refusal{Code: code, Field: field, Message: message}
}

type PortRange struct {
	Min spec.Port `toml:"min" json:"min"`
	Max spec.Port `toml:"max" json:"max"`
}
type Resources struct {
	MemoryMB  int `toml:"memory_mb" json:"memory_mb"`
	PIDsLimit int `toml:"pids_limit" json:"pids_limit"`
}
type Registry struct {
	Host               string   `toml:"host" json:"host"`
	RepositoryPrefixes []string `toml:"repository_prefixes" json:"repository_prefixes"`
}
type document struct {
	CaddyPort            uint16              `toml:"caddy_port" json:"caddy_port,omitempty"`
	SchemaVersion        int                 `toml:"schema_version" json:"schema_version"`
	Version              string              `toml:"version" json:"version"`
	AllowedRegistries    []Registry          `toml:"allowed_registries" json:"allowed_registries"`
	AllowedDomains       []string            `toml:"allowed_domains" json:"allowed_domains"`
	AppPorts             *PortRange          `toml:"app_ports" json:"app_ports"`
	AllowedSecrets       map[string][]string `toml:"allowed_secrets" json:"allowed_secrets"`
	Resources            Resources           `toml:"resources" json:"resources"`
	PersistentRoots      []string            `toml:"persistent_roots" json:"persistent_roots"`
	MinimumFreeDiskBytes *uint64             `toml:"minimum_free_disk_bytes" json:"minimum_free_disk_bytes"`
}

// Policy has no public constructor or writable fields. Its zero value refuses
// every normalization. Copies share only private, immutable data.
type Policy struct {
	config *document
	hash   string
}

func (p Policy) CaddyPort() uint16 {
	if p.config == nil {
		return 0
	}
	return p.config.CaddyPort
}
func (p Policy) MinimumFreeDiskBytes() uint64 {
	if p.config == nil {
		return 0
	}
	return *p.config.MinimumFreeDiskBytes
}
func (p Policy) Hash() string { return p.hash }
func (p Policy) Version() string {
	if p.config == nil {
		return ""
	}
	return p.config.Version
}
func (p Policy) CanonicalBytes() ([]byte, error) {
	if p.config == nil {
		return nil, refuse("policy.required", "$", "explicit operator policy is required")
	}
	return json.Marshal(p.config)
}
func (p Policy) PersistentRoots() []string {
	if p.config == nil {
		return nil
	}
	return slices.Clone(p.config.PersistentRoots)
}
func (p Policy) AppPorts() PortRange {
	if p.config == nil {
		return PortRange{}
	}
	return *p.config.AppPorts
}

var revisionPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,127}$`)
var referencePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,252}$`)

func Parse(data []byte) (Policy, error) {
	bad := func(code, field, message string) (Policy, error) { return Policy{}, refuse(code, field, message) }
	if len(data) > 1<<20 {
		return bad("policy.too_large", "$", "policy exceeds 1 MiB")
	}
	var raw document
	if err := toml.NewDecoder(bytes.NewReader(data)).DisallowUnknownFields().Decode(&raw); err != nil {
		return bad("policy.invalid_document", "$", "invalid policy syntax, field, type, or table shape")
	}
	var keys map[string]any
	if err := toml.Unmarshal(data, &keys); err != nil || !exactKeys(keys) {
		return bad("policy.invalid_document", "$", "invalid policy field or table shape")
	}
	if raw.SchemaVersion != SchemaVersion {
		return bad("policy.schema_version", "schema_version", "only schema version 1 is supported")
	}
	if !revisionPattern.MatchString(raw.Version) {
		return bad("policy.invalid_version", "version", "operator revision is required and must be a bounded token")
	}
	if raw.MinimumFreeDiskBytes == nil {
		minimum := DefaultMinimumFreeDiskBytes
		raw.MinimumFreeDiskBytes = &minimum
	}
	if *raw.MinimumFreeDiskBytes == 0 {
		return bad("policy.invalid_disk_minimum", "minimum_free_disk_bytes", "a positive minimum free disk size is required")
	}
	if raw.AppPorts == nil {
		raw.AppPorts = &PortRange{Min: 20000, Max: 20999}
	}
	if raw.AppPorts.Min < 1024 || raw.AppPorts.Max < raw.AppPorts.Min {
		return bad("policy.invalid_ports", "app_ports", "expected an ordered unprivileged port range")
	}
	if raw.Resources.MemoryMB < 1 || raw.Resources.MemoryMB > 2147483647 || raw.Resources.PIDsLimit < 1 || raw.Resources.PIDsLimit > 2147483647 {
		return bad("policy.invalid_resources", "resources", "positive 32-bit resource ceilings are required")
	}
	for i := range raw.AllowedRegistries {
		r := &raw.AllowedRegistries[i]
		if !cleanASCII(r.Host) || strings.ContainsAny(r.Host, "/@") {
			return bad("policy.invalid_registry", "allowed_registries", "expected an exact ASCII registry host with optional port")
		}
		r.Host = strings.ToLower(r.Host)
		if !validImage(r.Host, "probe") {
			return bad("policy.invalid_registry", "allowed_registries", "expected an exact ASCII registry host with optional port")
		}
		for _, prefix := range r.RepositoryPrefixes {
			if !validImage(r.Host, prefix) || strings.ContainsAny(prefix, ":@") {
				return bad("policy.invalid_registry", "allowed_registries", "expected lowercase repository path prefixes")
			}
		}
		r.RepositoryPrefixes = sortedUnique(r.RepositoryPrefixes)
	}
	slices.SortFunc(raw.AllowedRegistries, func(a, b Registry) int { return strings.Compare(a.Host, b.Host) })
	for i := 1; i < len(raw.AllowedRegistries); i++ {
		if raw.AllowedRegistries[i-1].Host == raw.AllowedRegistries[i].Host {
			return bad("policy.invalid_registry", "allowed_registries", "duplicate registry hosts are unsupported")
		}
	}
	if raw.AllowedRegistries == nil {
		raw.AllowedRegistries = []Registry{}
	}
	for i, d := range raw.AllowedDomains {
		if !cleanASCII(d) {
			return bad("policy.invalid_domain", "allowed_domains", "expected exact ASCII DNS names or wildcard suffixes without IDN")
		}
		d = strings.ToLower(d)
		if !validDomain(strings.TrimPrefix(d, "*.")) {
			return bad("policy.invalid_domain", "allowed_domains", "expected exact ASCII DNS names or wildcard suffixes without IDN")
		}
		raw.AllowedDomains[i] = d
	}
	raw.AllowedDomains = sortedUnique(raw.AllowedDomains)
	if raw.AllowedSecrets == nil {
		raw.AllowedSecrets = map[string][]string{}
	}
	for name, refs := range raw.AllowedSecrets {
		probe := probeApp()
		probe["name"] = name
		if _, e := parseMap(probe); e != nil {
			return bad("policy.invalid_secrets", "allowed_secrets", "expected valid app names")
		}
		for _, ref := range refs {
			if !referencePattern.MatchString(ref) {
				return bad("policy.invalid_secrets", "allowed_secrets", "expected secret reference names, never values")
			}
		}
		raw.AllowedSecrets[name] = sortedUnique(refs)
	}
	for _, root := range raw.PersistentRoots {
		if root == "/" || !strings.HasPrefix(root, "/") || path.Clean(root) != root || strings.ContainsAny(root, "\\") || !cleanASCII(root) {
			return bad("policy.invalid_roots", "persistent_roots", "expected clean absolute non-root ASCII data paths")
		}
	}
	raw.PersistentRoots = sortedUnique(raw.PersistentRoots)
	canonical, e := json.Marshal(raw)
	if e != nil {
		return bad("policy.invalid_document", "$", "policy could not be canonicalized")
	}
	sum := sha256.Sum256(canonical)
	return Policy{config: &raw, hash: "sha256:" + hex.EncodeToString(sum[:])}, nil
}

func exactKeys(keys map[string]any) bool {
	if !onlyKeys(keys, "schema_version", "version", "allowed_registries", "allowed_domains", "app_ports", "allowed_secrets", "resources", "persistent_roots", "minimum_free_disk_bytes", "caddy_port") {
		return false
	}
	for key, allowed := range map[string][]string{"app_ports": {"min", "max"}, "resources": {"memory_mb", "pids_limit"}} {
		if v, ok := keys[key]; ok {
			m, ok := v.(map[string]any)
			if !ok || !onlyKeys(m, allowed...) {
				return false
			}
		}
	}
	if v, ok := keys["allowed_registries"]; ok {
		rows, ok := v.([]any)
		if !ok {
			return false
		}
		for _, row := range rows {
			m, ok := row.(map[string]any)
			if !ok || !onlyKeys(m, "host", "repository_prefixes") {
				return false
			}
		}
	}
	return true
}
func onlyKeys(m map[string]any, allowed ...string) bool {
	for k := range m {
		if !slices.Contains(allowed, k) {
			return false
		}
	}
	return true
}
func sortedUnique(items []string) []string {
	out := append([]string{}, items...)
	slices.Sort(out)
	return slices.Compact(out)
}
func cleanASCII(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] < 33 || s[i] > 126 {
			return false
		}
	}
	return true
}
func probeApp() map[string]any {
	return map[string]any{"schema_version": 1, "name": "probe", "image": "registry.example.com/probe@sha256:" + strings.Repeat("a", 64), "container_port": 3000, "domains": []string{"probe.example.com"}}
}
func parseMap(m map[string]any) (spec.App, error) {
	b, e := toml.Marshal(m)
	if e != nil {
		return spec.App{}, e
	}
	return spec.Parse(b)
}
func validImage(host, repo string) bool {
	m := probeApp()
	m["image"] = host + "/" + repo + "@sha256:" + strings.Repeat("a", 64)
	_, e := parseMap(m)
	return e == nil
}
func validDomain(d string) bool {
	m := probeApp()
	m["domains"] = []string{d}
	_, e := parseMap(m)
	return e == nil
}
