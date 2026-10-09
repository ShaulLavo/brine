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
