package host

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/ShaulLavo/brine/internal/spec"
	"github.com/ShaulLavo/brine/internal/target"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func hashBytes(b []byte) string                                           { return fmt.Sprintf("sha256:%x", sha256.Sum256(b)) }
func TestRegistryVerifiesIndexManifestAndConfig(t *testing.T) {
	config := []byte(`{"os":"linux","architecture":"arm64"}`)
	manifest, _ := json.Marshal(map[string]any{"schemaVersion": 2, "config": map[string]string{"digest": hashBytes(config)}})
	index, _ := json.Marshal(map[string]any{"schemaVersion": 2, "manifests": []any{map[string]any{"digest": hashBytes(manifest), "platform": map[string]string{"os": "linux", "architecture": "arm64"}}}})
	for _, multi := range []bool{false, true} {
		t.Run(fmt.Sprint(multi), func(t *testing.T) {
			pin := manifest
			if multi {
				pin = index
			}
			values := map[string][]byte{hashBytes(pin): pin, hashBytes(manifest): manifest, hashBytes(config): config}
			calls := 0
			transport := roundTripFunc(func(req *http.Request) (*http.Response, error) {
				calls++
				digest := req.URL.Path[strings.LastIndex(req.URL.Path, "/")+1:]
				raw := values[digest]
				return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(string(raw))), Header: http.Header{}}, nil
			})
			image, err := (Registry{Transport: transport}).Resolve(context.Background(), spec.ImageReference("registry.example/team/hello@"+hashBytes(pin)), target.Platform{OS: "linux", Arch: "arm64"})
			if err != nil || image.Digest != hashBytes(pin) || image.ManifestDigest.Value == nil || *image.ManifestDigest.Value != hashBytes(manifest) {
				t.Fatalf("image %+v %v", image, err)
			}
			expected := 2
			if multi {
				expected = 3
			}
			if calls != expected {
				t.Fatalf("calls %d", calls)
			}
		})
	}
}
func TestRegistryRefusesUnverifiedBytesAndPlatform(t *testing.T) {
	for _, raw := range []string{`{"schemaVersion":2}`, `{"os":"linux","architecture":"amd64"}`} {
		transport := roundTripFunc(func(*http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(raw)), Header: http.Header{}}, nil
		})
		if _, err := (Registry{Transport: transport}).Resolve(context.Background(), spec.ImageReference("registry.example/team/hello@sha256:"+strings.Repeat("a", 64)), target.Platform{OS: "linux", Arch: "arm64"}); err == nil {
			t.Fatal("unverified bytes accepted")
		}
	}
	config := []byte(`{"os":"linux","architecture":"amd64"}`)
	manifest, _ := json.Marshal(map[string]any{"schemaVersion": 2, "config": map[string]string{"digest": hashBytes(config)}})
	transport := roundTripFunc(func(req *http.Request) (*http.Response, error) {
		raw := manifest
		if strings.Contains(req.URL.Path, "/blobs/") {
			raw = config
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(string(raw))), Header: http.Header{}}, nil
	})
	if _, err := (Registry{Transport: transport}).Resolve(context.Background(), spec.ImageReference("registry.example/team/hello@"+hashBytes(manifest)), target.Platform{OS: "linux", Arch: "arm64"}); err == nil {
		t.Fatal("wrong platform accepted")
	}
}
func TestRegistryRejectsCrossOriginTokenChallenge(t *testing.T) {
	calls := 0
	transport := roundTripFunc(func(*http.Request) (*http.Response, error) {
		calls++
		return &http.Response{StatusCode: 401, Body: io.NopCloser(strings.NewReader("")), Header: http.Header{"Www-Authenticate": []string{`Bearer realm="https://other.example/token",service="fixture"`}}}, nil
	})
	if _, err := (Registry{Transport: transport}).Resolve(context.Background(), spec.ImageReference("registry.example/team/hello@sha256:"+strings.Repeat("a", 64)), target.Platform{OS: "linux", Arch: "arm64"}); err == nil || calls != 1 {
		t.Fatalf("untrusted token request %d %v", calls, err)
	}
}
func TestRegistryAnonymousBearer(t *testing.T) {
	config := []byte(`{"os":"linux","architecture":"arm64"}`)
	manifest, _ := json.Marshal(map[string]any{"schemaVersion": 2, "config": map[string]string{"digest": hashBytes(config)}})
	calls := 0
	transport := roundTripFunc(func(req *http.Request) (*http.Response, error) {
		calls++
		raw := manifest
		status := 200
		header := http.Header{}
		switch {
		case req.URL.Path == "/token":
			if req.URL.Query().Get("scope") != "repository:team/hello:pull" {
				t.Fatal("bad scope")
			}
			raw = []byte(`{"token":"fixture-token"}`)
		case req.Header.Get("Authorization") == "":
			status = 401
			header.Set("WWW-Authenticate", `Bearer realm="https://registry.example/token",service="fixture",scope="ignored"`)
		case strings.Contains(req.URL.Path, "/blobs/"):
			raw = config
		}
		return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(string(raw))), Header: header}, nil
	})
	_, err := (Registry{Transport: transport}).Resolve(context.Background(), spec.ImageReference("registry.example/team/hello@"+hashBytes(manifest)), target.Platform{OS: "linux", Arch: "arm64"})
	if err != nil || calls != 4 {
		t.Fatalf("bearer %d %v", calls, err)
	}
}

func TestRegistryRedirectConfinement(t *testing.T) {
	original, _ := http.NewRequest(http.MethodGet, "https://ghcr.io/v2/team/hello/blobs/sha256:fixture", nil)
	for _, endpoint := range []string{"http://pkg-containers.githubusercontent.com/blob", "https://other.example/blob", "https://user:password@pkg-containers.githubusercontent.com/blob"} {
		req, _ := http.NewRequest(http.MethodGet, endpoint, nil)
		if registryRedirect(req, []*http.Request{original}) == nil {
			t.Fatal("untrusted redirect accepted")
		}
	}
	req, _ := http.NewRequest(http.MethodGet, "https://pkg-containers.githubusercontent.com/blob", nil)
	req.Header.Set("Authorization", "Bearer fixture-token")
	if err := registryRedirect(req, []*http.Request{original}); err != nil || req.Header.Get("Authorization") != "" {
		t.Fatal("CDN redirect forwarded credentials")
	}
	token, _ := http.NewRequest(http.MethodGet, "https://ghcr.io/token", nil)
	if registryRedirect(req, []*http.Request{token}) == nil {
		t.Fatal("token service redirected")
	}
}

func TestRegistryDockerHub(t *testing.T) {
	config := []byte(`{"os":"linux","architecture":"arm64"}`)
	manifest, _ := json.Marshal(map[string]any{"schemaVersion": 2, "config": map[string]string{"digest": hashBytes(config)}})
	transport := roundTripFunc(func(req *http.Request) (*http.Response, error) {
		status, raw, header := 200, manifest, http.Header{}
		if req.URL.Host == "auth.docker.io" {
			if req.URL.Path != "/token" || req.URL.Query().Get("service") != "registry.docker.io" || req.URL.Query().Get("scope") != "repository:library/fixture:pull" || req.Header.Get("Authorization") != "" {
				t.Fatalf("unexpected token request %s", req.URL)
			}
			raw = []byte(`{"token":"fixture-token"}`)
		} else {
			if req.URL.Host != "registry-1.docker.io" {
				t.Errorf("Docker Hub registry host = %s", req.URL.Host)
			}
			if req.Header.Get("Authorization") == "" {
				status = 401
				raw = nil
				header.Set("WWW-Authenticate", `Bearer realm="https://auth.docker.io/token",service="registry.docker.io"`)
			} else if strings.Contains(req.URL.Path, "/blobs/") {
				raw = config
			}
		}
		return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(string(raw))), Header: header}, nil
	})
	_, err := (Registry{Transport: transport}).Resolve(context.Background(), spec.ImageReference("docker.io/library/fixture@"+hashBytes(manifest)), target.Platform{OS: "linux", Arch: "arm64"})
	if err != nil {
		t.Fatal(err)
	}
}

func TestRegistryDockerHubRedirectConfinement(t *testing.T) {
	original, _ := http.NewRequest(http.MethodGet, "https://registry-1.docker.io/v2/library/fixture/blobs/sha256:fixture", nil)
	for _, host := range []string{"production.cloudfront.docker.com", "production.cloudflare.docker.com"} {
		req, _ := http.NewRequest(http.MethodGet, "https://"+host+"/blob", nil)
		req.Header.Set("Authorization", "Bearer fixture-token")
		if err := registryRedirect(req, []*http.Request{original}); err != nil || req.Header.Get("Authorization") != "" {
			t.Fatalf("Docker CDN %s rejected or received credentials: %v", host, err)
		}
	}
	for _, endpoint := range []string{"https://other.docker.com/blob", "https://production.cloudfront.docker.com:444/blob", "http://production.cloudfront.docker.com/blob"} {
		req, _ := http.NewRequest(http.MethodGet, endpoint, nil)
		if registryRedirect(req, []*http.Request{original}) == nil {
			t.Fatalf("untrusted Docker CDN accepted %s", endpoint)
		}
	}
	manifest, _ := http.NewRequest(http.MethodGet, "https://registry-1.docker.io/v2/library/fixture/manifests/sha256:fixture", nil)
	req, _ := http.NewRequest(http.MethodGet, "https://production.cloudfront.docker.com/blob", nil)
	if registryRedirect(req, []*http.Request{manifest}) == nil {
		t.Fatal("manifest redirected")
	}
}

func TestRegistryDockerHubTokenConfinement(t *testing.T) {
	transport := roundTripFunc(func(*http.Request) (*http.Response, error) {
		t.Fatal("untrusted token endpoint requested")
		return nil, nil
	})
	for _, tc := range []struct{ host, realm, service string }{
		{"registry.example", "https://auth.docker.io/token", "registry.docker.io"},
		{"registry-1.docker.io", "https://auth.docker.io/other", "registry.docker.io"},
		{"registry-1.docker.io", "https://auth.docker.io/token", "other"},
		{"registry-1.docker.io", "https://auth.docker.io:444/token", "registry.docker.io"},
	} {
		challenge := fmt.Sprintf(`Bearer realm="%s",service="%s"`, tc.realm, tc.service)
		if _, err := registryToken(context.Background(), &http.Client{Transport: transport}, challenge, tc.host, "library/fixture"); err == nil {
			t.Fatal("untrusted Docker token service accepted")
		}
	}
}

func TestRegistryDockerHubRepositoryNormalization(t *testing.T) {
	config := []byte(`{"os":"linux","architecture":"arm64"}`)
	manifest, _ := json.Marshal(map[string]any{"schemaVersion": 2, "config": map[string]string{"digest": hashBytes(config)}})
	for _, tc := range []struct{ name, repository string }{
		{"docker.io/alpine", "library/alpine"},
		{"docker.io/alpine:latest", "library/alpine"},
		{"docker.io/library/alpine", "library/alpine"},
		{"docker.io/team/alpine", "team/alpine"},
		{"registry.example/alpine", "alpine"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			host := "registry-1.docker.io"
			realm, service := "https://auth.docker.io/token", "registry.docker.io"
			if strings.HasPrefix(tc.name, "registry.example/") {
				host, realm, service = "registry.example", "https://registry.example/token", "fixture"
			}
			calls := 0
			transport := roundTripFunc(func(req *http.Request) (*http.Response, error) {
				calls++
				status, raw, header := http.StatusOK, manifest, http.Header{}
				if req.URL.Path == "/token" {
					if got := req.URL.Query().Get("scope"); got != "repository:"+tc.repository+":pull" {
						t.Errorf("token scope %q does not match repository %q", got, tc.repository)
					}
					raw = []byte(`{"token":"fixture-token"}`)
				} else {
					if req.URL.Host != host {
						t.Errorf("registry host = %q, want %q", req.URL.Host, host)
					}
					expected := "/v2/" + tc.repository + "/manifests/" + hashBytes(manifest)
					if req.Header.Get("Authorization") == "" {
						status, raw = http.StatusUnauthorized, nil
						header.Set("WWW-Authenticate", fmt.Sprintf(`Bearer realm="%s",service="%s"`, realm, service))
					} else if strings.HasSuffix(req.URL.Path, "/blobs/"+hashBytes(config)) {
						expected, raw = "/v2/"+tc.repository+"/blobs/"+hashBytes(config), config
					}
					if req.URL.Path != expected {
						t.Errorf("registry path = %q, want %q", req.URL.Path, expected)
					}
				}
				return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(string(raw))), Header: header}, nil
			})
			image, err := (Registry{Transport: transport}).Resolve(context.Background(), spec.ImageReference(tc.name+"@"+hashBytes(manifest)), target.Platform{OS: "linux", Arch: "arm64"})
			if err != nil || calls != 4 || image.Digest != hashBytes(manifest) {
				t.Fatalf("resolve image %+v calls %d err %v", image, calls, err)
			}
		})
	}
}

func TestRegistryRedirectUsesOperationSegment(t *testing.T) {
	for _, registry := range []struct{ host, cdn string }{
		{"registry-1.docker.io", "production.cloudfront.docker.com"},
		{"registry-1.docker.io", "production.cloudflare.docker.com"},
		{"ghcr.io", "pkg-containers.githubusercontent.com"},
	} {
		for _, tc := range []struct {
			path string
			blob bool
		}{
			{"/v2/team/blobs/manifests/sha256:fixture", false},
			{"/v2/team/blobs/nested/manifests/sha256:fixture", false},
			{"/v2/team/blobs/blobs/sha256:fixture", true},
			{"/v2/team/manifests/blobs/sha256:fixture", true},
			{"/v2/alpine/blobs/sha256:fixture", true},
			{"/token/blobs/sha256:fixture", false},
		} {
			t.Run(registry.host+tc.path+registry.cdn, func(t *testing.T) {
				original, _ := http.NewRequest(http.MethodGet, "https://"+registry.host+tc.path, nil)
				req, _ := http.NewRequest(http.MethodGet, "https://"+registry.cdn+"/blob", nil)
				req.Header.Set("Authorization", "Bearer fixture-token")
				err := registryRedirect(req, []*http.Request{original})
				if tc.blob {
					if err != nil || req.Header.Get("Authorization") != "" {
						t.Fatalf("blob redirect failed or forwarded credentials: %v", err)
					}
				} else if err != http.ErrUseLastResponse {
					t.Fatalf("non-blob request redirected: %v", err)
				}
			})
		}
	}
}
