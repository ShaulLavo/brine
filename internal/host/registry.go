package host

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/ShaulLavo/brine/internal/plan"
	"github.com/ShaulLavo/brine/internal/podman"
	"github.com/ShaulLavo/brine/internal/spec"
	"github.com/ShaulLavo/brine/internal/target"
)

// Registry verifies raw registry bytes. Podman's formatted manifest inspection
// cannot prove a content digest. No image is pulled while planning.
type Registry struct{ Transport http.RoundTripper }

var digestPattern = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
var bearerPattern = regexp.MustCompile(`^Bearer realm="([^"]+)",service="([^"]+)"(?:,scope="[^"]*")?$`)

func (r Registry) Resolve(ctx context.Context, ref spec.ImageReference, platform target.Platform) (plan.Image, error) {
	var out plan.Image
	pin, err := podman.ParseImage(string(ref))
	if err != nil {
		return out, err
	}
	name, digest, _ := strings.Cut(pin.String(), "@")
	host, repository, _ := strings.Cut(name, "/")
	// A tag accompanying the immutable pin does not enter the registry API path.
	repository = strings.Split(repository, ":")[0]
	if host == "docker.io" {
		host = "registry-1.docker.io"
		if !strings.Contains(repository, "/") {
			repository = "library/" + repository
		}
	}
	if platform.OS != "linux" || (platform.Arch != "amd64" && platform.Arch != "arm64") {
		return out, errors.New("host: unsupported image platform")
	}
	transport := r.Transport
	if transport == nil {
		transport = &http.Transport{Proxy: nil, TLSHandshakeTimeout: 10 * time.Second, DisableKeepAlives: true}
	}
	client := &http.Client{Transport: transport, Timeout: 30 * time.Second, CheckRedirect: registryRedirect}
	ctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	base := "https://" + host + "/v2/" + repository
	token := ""
	fetch := func(kind, digest string) ([]byte, error) {
		return registryFetch(ctx, client, base+"/"+kind+"/"+digest, digest, &token, host, repository)
	}
	raw, err := fetch("manifests", digest)
	if err != nil {
		return out, err
	}
	var manifest struct {
		SchemaVersion int    `json:"schemaVersion"`
		MediaType     string `json:"mediaType"`
		Manifests     []struct {
			Digest   string `json:"digest"`
			Platform struct {
				OS      string `json:"os"`
				Arch    string `json:"architecture"`
				Variant string `json:"variant"`
			} `json:"platform"`
		} `json:"manifests"`
		Config struct {
			Digest string `json:"digest"`
		} `json:"config"`
	}
	if json.Unmarshal(raw, &manifest) != nil || manifest.SchemaVersion != 2 {
		return out, errors.New("host: invalid registry manifest")
	}
	selected := digest
	if len(manifest.Manifests) > 0 {
		selected = ""
		for _, m := range manifest.Manifests {
			if m.Platform.OS == platform.OS && m.Platform.Arch == platform.Arch && (m.Platform.Variant == "" || platform.Arch == "arm64" && m.Platform.Variant == "v8") {
				if selected != "" {
					return out, errors.New("host: ambiguous image platform")
				}
				selected = m.Digest
			}
		}
		if !digestPattern.MatchString(selected) {
			return out, errors.New("host: image platform missing")
		}
		raw, err = fetch("manifests", selected)
		if err != nil {
			return out, err
		}
		manifest.Manifests = nil
		manifest.Config.Digest = ""
		if json.Unmarshal(raw, &manifest) != nil || manifest.SchemaVersion != 2 || len(manifest.Manifests) > 0 {
			return out, errors.New("host: invalid selected manifest")
		}
	}
	if !digestPattern.MatchString(manifest.Config.Digest) {
		return out, errors.New("host: image config missing")
	}
	raw, err = fetch("blobs", manifest.Config.Digest)
	if err != nil {
		return out, err
	}
	var config struct {
		OS      string `json:"os"`
		Arch    string `json:"architecture"`
		Variant string `json:"variant"`
	}
	if json.Unmarshal(raw, &config) != nil || config.OS != platform.OS || config.Arch != platform.Arch || config.Variant != "" && !(platform.Arch == "arm64" && config.Variant == "v8") {
		return out, errors.New("host: image platform mismatch")
	}
	return plan.Image{Digest: digest, Platform: platform, ManifestDigest: target.Known(selected)}, nil
}

func registryFetch(ctx context.Context, client *http.Client, endpoint, digest string, token *string, host, repository string) ([]byte, error) {
	if !digestPattern.MatchString(digest) {
		return nil, errors.New("host: invalid registry digest")
	}
	for attempt := 0; attempt < 2; attempt++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Accept", "application/vnd.oci.image.index.v1+json, application/vnd.oci.image.manifest.v1+json, application/vnd.docker.distribution.manifest.list.v2+json, application/vnd.docker.distribution.manifest.v2+json")
		if *token != "" {
			req.Header.Set("Authorization", "Bearer "+*token)
		}
		res, err := client.Do(req)
		if err != nil {
			return nil, errors.New("host: registry request failed")
		}
		if res.StatusCode == http.StatusUnauthorized && attempt == 0 && *token == "" {
			challenge := res.Header.Get("WWW-Authenticate")
			res.Body.Close()
			*token, err = registryToken(ctx, client, challenge, host, repository)
			if err != nil {
				return nil, err
			}
			continue
		}
		raw, readErr := io.ReadAll(io.LimitReader(res.Body, (4<<20)+1))
		res.Body.Close()
		if readErr != nil || res.StatusCode != http.StatusOK || len(raw) > 4<<20 || fmt.Sprintf("sha256:%x", sha256.Sum256(raw)) != digest {
			return nil, errors.New("host: registry digest verification failed")
		}
		return raw, nil
	}
	return nil, errors.New("host: registry authentication failed")
}
func registryRedirect(req *http.Request, via []*http.Request) error {
	if len(via) == 0 || len(via) > 3 || req.URL.Scheme != "https" || req.URL.User != nil {
		return http.ErrUseLastResponse
	}
	original := via[0]
	// Registry endpoints are /v2/<repository>/<operation>/<reference>;
	// a repository segment named "blobs" does not make a manifest a blob.
	segments := strings.Split(original.URL.Path, "/")
	if !strings.HasPrefix(original.URL.Path, "/v2/") || len(segments) < 5 || segments[len(segments)-2] != "blobs" || segments[len(segments)-1] == "" {
		return http.ErrUseLastResponse
	}
	if req.URL.Host != original.URL.Host {
		githubCDN := original.URL.Host == "ghcr.io" && req.URL.Host == "pkg-containers.githubusercontent.com"
		dockerCDN := original.URL.Host == "registry-1.docker.io" && (req.URL.Host == "production.cloudfront.docker.com" || req.URL.Host == "production.cloudflare.docker.com")
		if !githubCDN && !dockerCDN {
			return http.ErrUseLastResponse
		}
		req.Header.Del("Authorization")
	}
	return nil
}

func registryToken(ctx context.Context, client *http.Client, challenge, host, repository string) (string, error) {
	m := bearerPattern.FindStringSubmatch(challenge)
	if m == nil {
		return "", errors.New("host: unsupported registry authentication")
	}
	realm, err := url.Parse(m[1])
	// Docker Hub's anonymous token service is separate from its registry origin.
	// No operator credentials or arbitrary challenge destinations are consulted.
	if err != nil || realm.Scheme != "https" || realm.User != nil || realm.Fragment != "" {
		return "", errors.New("host: untrusted registry token service")
	}
	dockerToken := host == "registry-1.docker.io" && realm.Host == "auth.docker.io" && realm.Path == "/token" && m[2] == "registry.docker.io"
	if realm.Host != host && !dockerToken {
		return "", errors.New("host: untrusted registry token service")
	}
	q := realm.Query()
	q.Set("service", m[2])
	q.Set("scope", "repository:"+repository+":pull")
	realm.RawQuery = q.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, realm.String(), nil)
	if err != nil {
		return "", err
	}
	res, err := client.Do(req)
	if err != nil {
		return "", errors.New("host: registry token request failed")
	}
	defer res.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(res.Body, (64<<10)+1))
	var t struct {
		Token       string `json:"token"`
		AccessToken string `json:"access_token"`
	}
	if err != nil || res.StatusCode != http.StatusOK || len(raw) > 64<<10 || json.Unmarshal(raw, &t) != nil {
		return "", errors.New("host: registry token invalid")
	}
	if t.Token == "" {
		t.Token = t.AccessToken
	}
	if t.Token == "" || len(t.Token) > 32<<10 || strings.ContainsAny(t.Token, "\r\n") {
		return "", errors.New("host: registry token invalid")
	}
	return t.Token, nil
}
