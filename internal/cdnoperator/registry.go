package cdnoperator

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"regexp"

	"github.com/ShaulLavo/brine/internal/cdn"
	"github.com/ShaulLavo/brine/internal/cdn/cloudflare"
	"github.com/ShaulLavo/brine/internal/strictjson"
)

const registryLimit = 1 << 20

type Registry struct {
	profiles map[cdn.ProfileRef]registeredProfile
}

type registeredProfile struct {
	profile cloudflare.Profile
	scope   cdn.ProfileScope
	tokens  cloudflare.TokenSource
}

var environmentName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,127}$`)

// LoadRegistry reads explicit local profiles and credential references only.
func LoadRegistry(path string) (Registry, error) {
	before, err := os.Lstat(path)
	if err != nil || !before.Mode().IsRegular() || before.Mode().Perm() != 0600 || before.Size() > registryLimit {
		return Registry{}, cloudflare.ErrProfile
	}
	file, err := os.Open(path) // #nosec G304 -- Explicit operator registry path; identity, type and private mode checked before and after open.
	if err != nil {
		return Registry{}, cloudflare.ErrProfile
	}
	defer func() { _ = file.Close() }()
	actual, err := file.Stat()
	if err != nil || !os.SameFile(before, actual) || actual.Mode().Perm() != 0600 {
		return Registry{}, cloudflare.ErrProfile
	}
	raw, err := io.ReadAll(io.LimitReader(file, registryLimit+1))
	if err != nil || len(raw) > registryLimit {
		return Registry{}, cloudflare.ErrProfile
	}
	return ParseRegistry(raw, filepath.Dir(path))
}

func ParseRegistry(raw []byte, base string) (Registry, error) {
	fields, err := strictjson.Object(raw, "schema_version", "profiles")
	if err != nil || len(raw) > registryLimit {
		return Registry{}, cloudflare.ErrProfile
	}
	version, err := strictjson.Value[int](fields["schema_version"])
	if err != nil || version != 1 {
		return Registry{}, cloudflare.ErrProfile
	}
	entries, err := strictjson.Value[[]json.RawMessage](fields["profiles"])
	if err != nil || len(entries) == 0 || len(entries) > 64 {
		return Registry{}, cloudflare.ErrProfile
	}
	registry := Registry{profiles: make(map[cdn.ProfileRef]registeredProfile, len(entries))}
	for _, entry := range entries {
		name, profile, err := decodeRegisteredProfile(entry, base)
		if err != nil || registry.profiles[name].tokens != nil {
			return Registry{}, cloudflare.ErrProfile
		}
		registry.profiles[name] = profile
	}
	return registry, nil
}

func decodeRegisteredProfile(raw []byte, base string) (cdn.ProfileRef, registeredProfile, error) {
	fields, err := strictjson.Object(raw, "name", "driver", "configuration", "credential")
	if err != nil {
		return "", registeredProfile{}, cloudflare.ErrProfile
	}
	name, nameErr := strictjson.Value[cdn.ProfileRef](fields["name"])
	driver, driverErr := strictjson.Value[string](fields["driver"])
	profile, profileErr := cloudflare.ParseProfile(fields["configuration"])
	if nameErr != nil || driverErr != nil || driver != "cloudflare" || profileErr != nil {
		return "", registeredProfile{}, cloudflare.ErrProfile
	}
	scope, err := profile.Scope(name)
	if err != nil {
		return "", registeredProfile{}, err
	}
	var configuration struct {
		CredentialRef string `json:"credential_ref"`
	}
	if json.Unmarshal(fields["configuration"], &configuration) != nil {
		return "", registeredProfile{}, cloudflare.ErrProfile
	}
	tokens, err := credentialSource(fields["credential"], configuration.CredentialRef, base)
	return name, registeredProfile{profile: profile, scope: scope, tokens: tokens}, err
}

func credentialSource(raw []byte, reference, base string) (cloudflare.TokenSource, error) {
	if fields, err := strictjson.Object(raw, "kind", "path"); err == nil {
		kind, kindErr := strictjson.Value[string](fields["kind"])
		path, pathErr := strictjson.Value[string](fields["path"])
		if kindErr != nil || kind != "file" || pathErr != nil || path == "" {
			return nil, cloudflare.ErrCredential
		}
		if !filepath.IsAbs(path) {
			path = filepath.Join(base, path)
		}
		return cloudflare.FileToken(reference, path), nil
	}
	fields, err := strictjson.Object(raw, "kind", "name")
	if err != nil {
		return nil, cloudflare.ErrCredential
	}
	kind, kindErr := strictjson.Value[string](fields["kind"])
	name, nameErr := strictjson.Value[string](fields["name"])
	if kindErr != nil || kind != "environment" || nameErr != nil || !environmentName.MatchString(name) {
		return nil, cloudflare.ErrCredential
	}
	return func(ctx context.Context, requested string) (string, error) {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		value := os.Getenv(name)
		if requested != reference || value == "" || len(value) > 4096 {
			return "", cloudflare.ErrCredential
		}
		return value, nil
	}, nil
}

func (r Registry) lookup(scope cdn.ProfileScope) (registeredProfile, error) {
	profile, ok := r.profiles[scope.Reference]
	if !ok || profile.scope != scope {
		return registeredProfile{}, cloudflare.ErrProfile
	}
	return profile, nil
}

func (r Registry) Edge(scope cdn.ProfileScope) (cdn.EdgeDriver, error) {
	profile, err := r.lookup(scope)
	if err != nil {
		return nil, err
	}
	return cloudflare.NewEdge(profile.profile, scope.Reference, profile.tokens)
}

func (r Registry) Traffic(scope cdn.ProfileScope) (cdn.TrafficDriver, error) {
	profile, err := r.lookup(scope)
	if err != nil {
		return nil, err
	}
	return cloudflare.NewTraffic(profile.profile, scope.Reference, profile.tokens)
}
