package spec

import (
	"bytes"
	"errors"
	"fmt"
	"net/netip"
	"net/url"
	"path"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/ShaulLavo/brine/internal/data"
	"github.com/pelletier/go-toml/v2"
)

// MaxStartupDeadlineSeconds is the shared validation and orchestration bound.
const MaxStartupDeadlineSeconds = 3600

// MaxHealthTimeoutSeconds bounds one HTTP request within startup health.
const MaxHealthTimeoutSeconds = 300

type Name string
type Domain string
type ImageReference string
type Port uint16
type HealthPath string
type SecretReference string

type RuntimeIdentity = data.RuntimeIdentity
type Database = data.Database
type SchemaCompatibility = data.SchemaCompatibility
type SchemaDefinition = data.SchemaDefinition

type App struct {
	Runtime             *RuntimeIdentity
	Databases           []Database
	SchemaCompatibility []SchemaCompatibility
	SchemaDefinitions   []SchemaDefinition
	SchemaVersion       int
	Name                Name
	Image               ImageReference
	ContainerPort       Port
	Domains             []Domain
	Health              Health
	Resources           *Resources
	Environment         map[string]string
	Secrets             map[string]SecretReference
}

type Health struct {
	Path                   HealthPath
	ExpectedStatus         int
	StartupDeadlineSeconds int
	TimeoutSeconds         int
}

type Resources struct {
	MemoryMB  int
	PIDsLimit int
}

// Error contains only parser-owned diagnostics, not TOML values or decoder errors.
type Error struct {
	Code    string
	Field   string
	Message string
}

func (e *Error) Error() string { return e.Code + " at " + e.Field + ": " + e.Message }
func refusal(code, field, message string) error {
	return &Error{Code: code, Field: field, Message: message}
}

type rawApp struct {
	SchemaVersion any            `toml:"schema_version"`
	Name          any            `toml:"name"`
	Image         any            `toml:"image"`
	ContainerPort any            `toml:"container_port"`
	Domains       any            `toml:"domains"`
	Health        *rawHealth     `toml:"health"`
	Resources     *rawResources  `toml:"resources"`
	Environment   map[string]any `toml:"environment"`
	Secrets       map[string]any `toml:"secrets"`
}
type rawHealth struct {
	Path                   any `toml:"path"`
	ExpectedStatus         any `toml:"expected_status"`
	StartupDeadlineSeconds any `toml:"startup_deadline_seconds"`
	TimeoutSeconds         any `toml:"timeout_seconds"`
}
type rawResources struct {
	MemoryMB  any `toml:"memory_mb"`
	PIDsLimit any `toml:"pids_limit"`
}

var label = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]*[a-z0-9])?$`)
var envName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
var secretName = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]{0,252}$`)
var repositoryPart = regexp.MustCompile(`^[a-z0-9]+(?:(?:[._]|__|-+)[a-z0-9]+)*$`)
var tag = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_.-]{0,127}$`)
var digest = regexp.MustCompile(`^[a-fA-F0-9]{64}$`)

// Parse returns a zero App on every refusal. Input is bounded to 1 MiB.
func Parse(data []byte) (App, error) {
	if len(data) > 1<<20 {
		return App{}, refusal("spec.too_large", "$", "app definition exceeds 1 MiB")
	}
	var raw rawApp
	err := toml.NewDecoder(bytes.NewReader(data)).DisallowUnknownFields().Decode(&raw)
	if err != nil {
		var unknown *toml.StrictMissingError
		if errors.As(err, &unknown) && len(unknown.Errors) > 0 {
			return App{}, refusal("spec.unknown_field", unknownPath(unknown.Errors[0].Key()), "unsupported field")
		}
		return App{}, refusal("spec.invalid_toml", "$", "invalid TOML syntax, duplicate key, or table shape")
	}
	// The library matches struct fields case-insensitively. Inspect exact keys
	// separately so aliases cannot override a field in this versioned schema.
	var keys map[string]any
	if err := toml.Unmarshal(data, &keys); err != nil {
		return App{}, refusal("spec.invalid_toml", "$", "invalid TOML document")
	}
	if err := exactFields(keys, "$"); err != nil {
		return App{}, err
	}
	app, err := validate(raw)
	if err != nil {
		return App{}, err
	}
	return app, nil
}

var schemaFields = map[string][]string{
	"$":           {"schema_version", "name", "image", "container_port", "domains", "health", "resources", "environment", "secrets"},
	"$.health":    {"path", "expected_status", "startup_deadline_seconds", "timeout_seconds"},
	"$.resources": {"memory_mb", "pids_limit"},
}

func exactFields(fields map[string]any, prefix string) error {
	for _, key := range sortedKeys(fields) {
		if !slices.Contains(schemaFields[prefix], key) {
			return refusal("spec.unknown_field", prefix+".[unknown]", "unsupported field")
		}
		nestedPrefix := prefix + "." + key
		if _, ok := schemaFields[nestedPrefix]; ok {
			if nested, ok := fields[key].(map[string]any); ok {
				if err := exactFields(nested, nestedPrefix); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

func unknownPath(key toml.Key) string {
	// Unknown names can themselves be credentials or terminal escape sequences.
	if len(key) > 1 && (key[0] == "health" || key[0] == "resources") {
		return "$." + key[0] + ".[unknown]"
	}
	return "$.[unknown]"
}

func text(value any, field string) (string, error) {
	if value == nil {
		return "", refusal("spec.required", field, "required field is missing")
	}
	s, ok := value.(string)
	if !ok {
		return "", refusal("spec.invalid_type", field, "expected a string")
	}
	return s, nil
}
func integer(value any, field string) (int64, error) {
	if value == nil {
		return 0, refusal("spec.required", field, "required field is missing")
	}
	n, ok := value.(int64)
	if !ok {
		return 0, refusal("spec.invalid_type", field, "expected an integer")
	}
	return n, nil
}

func validate(raw rawApp) (App, error) {
	var app App
	version, err := integer(raw.SchemaVersion, "schema_version")
	if err != nil {
		return app, err
	}
	if version != 1 {
		return app, refusal("spec.schema_version", "schema_version", "only schema version 1 is supported")
	}
	app.SchemaVersion = 1
	name, err := text(raw.Name, "name")
	if err != nil {
		return app, err
	}
	if len(name) > 63 || !label.MatchString(name) {
		return app, refusal("spec.invalid_name", "name", "expected a lowercase DNS label of 1 to 63 bytes")
	}
	app.Name = Name(name)
	image, err := text(raw.Image, "image")
	if err != nil {
		return app, err
	}
	normalized, ok := imageReference(image)
	if !ok {
		return app, refusal("spec.invalid_image", "image", "expected an explicit OCI registry and repository pinned to a sha256 digest")
	}
	app.Image = ImageReference(normalized)
	port, err := integer(raw.ContainerPort, "container_port")
	if err != nil {
		return app, err
	}
	if port < 1024 || port > 65535 {
		return app, refusal("spec.invalid_port", "container_port", "port must be between 1024 and 65535")
	}
	app.ContainerPort = Port(port)
	if raw.Domains == nil {
		return app, refusal("spec.required", "domains", "required field is missing")
	}
	domains, ok := raw.Domains.([]any)
	if !ok {
		return app, refusal("spec.invalid_type", "domains", "expected an array of strings")
	}
	if len(domains) == 0 {
		return app, refusal("spec.invalid_domain", "domains", "at least one domain is required")
	}
	seen := map[string]bool{}
	for i, value := range domains {
		field := fmt.Sprintf("domains[%d]", i)
		s, err := text(value, field)
		if err != nil {
			return app, err
		}
		if !isASCII(s) {
			return app, refusal("spec.invalid_domain", field, "expected an ASCII DNS domain without IDN labels")
		}
		s = strings.ToLower(s)
		if !validDomain(s) || seen[s] {
			return app, refusal("spec.invalid_domain", field, "expected a unique ASCII DNS domain without IDN labels")
		}
		seen[s] = true
		app.Domains = append(app.Domains, Domain(s))
	}
	app.Health = Health{Path: "/", ExpectedStatus: 200, StartupDeadlineSeconds: 30, TimeoutSeconds: 3}
	if raw.Health != nil {
		h := raw.Health
		if h.Path != nil {
			s, err := text(h.Path, "health.path")
			if err != nil {
				return app, err
			}
			if !validHealthPath(s) {
				return app, refusal("spec.invalid_health_path", "health.path", "expected an absolute clean HTTP path without escapes, query, or fragment")
			}
			app.Health.Path = HealthPath(s)
		}
		fields := []struct {
			value    any
			name     string
			min, max int64
			dest     *int
		}{
			{h.ExpectedStatus, "expected_status", 100, 599, &app.Health.ExpectedStatus},
			{h.StartupDeadlineSeconds, "startup_deadline_seconds", 1, MaxStartupDeadlineSeconds, &app.Health.StartupDeadlineSeconds},
			{h.TimeoutSeconds, "timeout_seconds", 1, MaxHealthTimeoutSeconds, &app.Health.TimeoutSeconds},
		}
		for _, f := range fields {
			if f.value == nil {
				continue
			}
			n, err := integer(f.value, "health."+f.name)
			if err != nil {
				return app, err
			}
			if n < f.min || n > f.max {
				return app, refusal("spec.invalid_health", "health."+f.name, "health setting is outside the supported range")
			}
			*f.dest = int(n)
		}
		if app.Health.TimeoutSeconds > app.Health.StartupDeadlineSeconds {
			return app, refusal("spec.invalid_health", "health.timeout_seconds", "timeout exceeds startup deadline")
		}
	}
	if raw.Resources != nil {
		r := Resources{}
		fields := []struct {
			value any
			name  string
			dest  *int
		}{{raw.Resources.MemoryMB, "memory_mb", &r.MemoryMB}, {raw.Resources.PIDsLimit, "pids_limit", &r.PIDsLimit}}
		for _, f := range fields {
			n, err := integer(f.value, "resources."+f.name)
			if err != nil {
				return app, err
			}
			if n < 1 || n > 2147483647 {
				return app, refusal("spec.invalid_resources", "resources."+f.name, "resource limit must be a positive 32-bit integer")
			}
			*f.dest = int(n)
		}
		app.Resources = &r
	}
	app.Environment = map[string]string{}
	app.Secrets = map[string]SecretReference{}
	for _, key := range sortedKeys(raw.Environment) {
		if !envName.MatchString(key) {
			return app, refusal("spec.invalid_environment_key", "environment", "expected a valid environment variable name")
		}
		if _, ok := raw.Secrets[key]; ok {
			return app, refusal("spec.environment_secret_collision", "environment", "environment and secrets must not share keys")
		}
		value, err := text(raw.Environment[key], "environment")
		if err != nil {
			return app, err
		}
		if strings.Contains(value, "${") {
			return app, refusal("spec.environment_expansion", "environment", "environment expansion is not supported")
		}
		if strings.IndexByte(value, 0) >= 0 {
			return app, refusal("spec.invalid_environment_value", "environment", "environment values cannot contain NUL")
		}
		if !utf8.ValidString(value) {
			return app, refusal("spec.invalid_environment_value", "environment", "environment values must be valid UTF-8")
		}
		for _, r := range value {
			if (unicode.IsControl(r) || unicode.IsSpace(r)) && r != ' ' && r != '\n' && r != '\r' && r != '\t' {
				return app, refusal("spec.invalid_environment_value", "environment", "unsupported environment control or whitespace character")
			}
		}
		app.Environment[key] = value
	}
	for _, key := range sortedKeys(raw.Secrets) {
		if !envName.MatchString(key) {
			return app, refusal("spec.invalid_environment_key", "secrets", "expected a valid environment variable name")
		}
		value, err := text(raw.Secrets[key], "secrets")
		if err != nil {
			return app, err
		}
		if !secretName.MatchString(value) {
			return app, refusal("spec.invalid_secret_reference", "secrets", "expected a Podman secret reference name, never a value")
		}
		app.Secrets[key] = SecretReference(value)
	}
	return app, nil
}
func sortedKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}
func isASCII(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] > 127 {
			return false
		}
	}
	return true
}
func validDomain(s string) bool {
	if len(s) > 253 || len(strings.Split(s, ".")) < 2 {
		return false
	}
	if _, err := netip.ParseAddr(s); err == nil {
		return false
	}
	for _, part := range strings.Split(s, ".") {
		if len(part) > 63 || strings.HasPrefix(part, "xn--") || !label.MatchString(part) {
			return false
		}
	}
	return true
}
func imageReference(s string) (string, bool) {
	parts := strings.Split(s, "@sha256:")
	if len(parts) != 2 || !digest.MatchString(parts[1]) {
		return "", false
	}
	ref := parts[0]
	slash := strings.IndexByte(ref, '/')
	if slash < 1 {
		return "", false
	}
	registry, repo := ref[:slash], ref[slash+1:]
	if colon := strings.LastIndexByte(registry, ':'); colon >= 0 {
		p := registry[colon+1:]
		n, err := strconv.Atoi(p)
		if err != nil || n < 1 || n > 65535 || strconv.Itoa(n) != p {
			return "", false
		}
		registry = registry[:colon]
	}
	if registry != strings.ToLower(registry) || !validDomain(registry) {
		return "", false
	}
	if colon := strings.LastIndexByte(repo, ':'); colon >= 0 {
		if !tag.MatchString(repo[colon+1:]) {
			return "", false
		}
		repo = repo[:colon]
	}
	if len(repo) > 255 {
		return "", false
	}
	for _, component := range strings.Split(repo, "/") {
		if !repositoryPart.MatchString(component) {
			return "", false
		}
	}
	return ref + "@sha256:" + strings.ToLower(parts[1]), true
}
func validHealthPath(s string) bool {
	if !strings.HasPrefix(s, "/") || strings.HasPrefix(s, "//") || path.Clean(s) != s || strings.ContainsAny(s, "%\\?#") {
		return false
	}
	for _, r := range s {
		if r > 127 || unicode.IsControl(r) || unicode.IsSpace(r) {
			return false
		}
	}
	u, err := url.ParseRequestURI(s)
	return err == nil && u.Path == s && u.RawQuery == "" && u.Fragment == ""
}

// ValidEnvironmentName validates edit keys even when an unset removes them.
func ValidEnvironmentName(name string) bool { return envName.MatchString(name) }
