// Package cdntest provides a serving, loopback-only CDN contract fixture.
package cdntest

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ShaulLavo/brine/internal/cdn"
)

const bodyLimit = 64 << 10
const publicHost = "game.example.test"

type Response struct {
	Status int
	Header http.Header
	Body   []byte
}

type Gate struct {
	Started  chan struct{}
	Release  chan struct{}
	Finished chan struct{}
}

type node struct {
	profile     cdn.ProfileScope
	resource    cdn.OwnedResource
	fingerprint cdn.Fingerprint
	attachment  cdn.Attachment
	version     uint64
	cache       map[string]Response
	url         string
	gate        *Gate
	drop        bool
}

type nativeOperation struct {
	Permit   cdn.Permit        `json:"permit"`
	Complete bool              `json:"complete"`
	Refused  bool              `json:"refused"`
	Resource cdn.OwnedResource `json:"resource"`
}

type Fixture struct {
	EdgeA, EdgeB, Traffic *Driver
	OriginURL, GatewayURL string
	Sample                cdn.AssetSample
	Client                *http.Client
	mu                    sync.Mutex
	nodes                 map[cdn.ProfileRef]*node
	operations            map[cdn.EffectID]*nativeOperation
	origin                map[string]Response
	hits                  map[string]int
	target                cdn.ProfileRef
	servers               []*httptest.Server
	ctx                   context.Context
	cancel                context.CancelFunc
	wg                    sync.WaitGroup
}

func New(t testing.TB) *Fixture {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	f := &Fixture{ctx: ctx, cancel: cancel, nodes: map[cdn.ProfileRef]*node{}, operations: map[cdn.EffectID]*nativeOperation{}, origin: map[string]Response{}, hits: map[string]int{}}
	t.Cleanup(f.close)
	f.OriginURL = f.start(http.HandlerFunc(f.serveOrigin))
	transport := f.servers[0].Client().Transport.(*http.Transport).Clone()
	transport.DisableCompression = true
	transport.TLSClientConfig = transport.TLSClientConfig.Clone()
	transport.TLSClientConfig.MinVersion = tls.VersionTLS12
	f.Client = &http.Client{Transport: transport, Timeout: 2 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	control := f.start(http.HandlerFunc(f.serveControl))
	f.EdgeA = f.driver("edge-a", false, control)
	f.EdgeB = f.driver("edge-b", false, control)
	f.Traffic = f.driver("traffic", true, control)
	f.nodes[f.EdgeA.profile.Reference].url = f.start(http.HandlerFunc(f.edgeHandler(f.EdgeA)))
	f.nodes[f.EdgeB.profile.Reference].url = f.start(http.HandlerFunc(f.edgeHandler(f.EdgeB)))
	f.GatewayURL = f.start(http.HandlerFunc(f.serveGateway))
	body := []byte("synthetic immutable fixture bytes\n")
	digest := cdn.Hash(body)
	f.Sample = cdn.AssetSample{Path: "/pack/blobs/" + strings.TrimPrefix(string(digest), "sha256:") + ".bin", Digest: digest}
	f.SetOrigin(f.Sample.Path, Response{Status: 200, Header: http.Header{"Content-Type": {"application/octet-stream"}, "Cache-Control": {"public, max-age=31536000, immutable"}}, Body: body})
	f.SetOrigin("/pack/index.json", Response{Status: 200, Header: http.Header{"Content-Type": {"application/json"}, "Cache-Control": {"no-store"}}, Body: []byte(`{"fixture":1}`)})
	return f
}

func (f *Fixture) start(handler http.Handler) string {
	s := httptest.NewUnstartedServer(handler)
	s.Config.ReadHeaderTimeout = time.Second
	s.Config.WriteTimeout = 3 * time.Second
	f.servers = append(f.servers, s)
	s.StartTLS()
	return s.URL
}

func (f *Fixture) close() {
	f.cancel()
	for _, server := range f.servers {
		server.CloseClientConnections()
		server.Close()
	}
	f.wg.Wait()
	if f.Client != nil {
		f.Client.CloseIdleConnections()
	}
}

func (f *Fixture) driver(name string, traffic bool, control string) *Driver {
	profile := cdn.ProfileScope{Reference: cdn.ProfileRef(name), Driver: "local-http", Fingerprint: cdn.Hash([]byte(name))}
	f.nodes[profile.Reference] = &node{profile: profile, fingerprint: cdn.Hash([]byte("initial:" + name)), cache: map[string]Response{}}
	return &Driver{fixture: f, profile: profile, traffic: traffic, control: control}
}

func (f *Fixture) Attachment(profile cdn.ProfileRef) cdn.Attachment {
	f.mu.Lock()
	defer f.mu.Unlock()
	selected := f.nodes[profile].profile
	target := cdn.Hash([]byte("fixture-target"))
	binding := cdn.Binding{Reference: "public", App: "fixture", Target: target, Hostname: publicHost, Origin: cdn.Origin{Address: f.OriginURL, Host: publicHost, TLSName: "example.com"}, Cache: cdn.CachePolicy{Prefix: "/pack/blobs/", Extensions: []string{"bin"}, TTLSeconds: cdn.ImmutableTTL}, AllowedEdges: []cdn.ProfileScope{f.EdgeA.profile, f.EdgeB.profile}, Traffic: f.Traffic.profile, TrafficAuthority: cdn.TrafficAuthority{ResourceID: "fixture-hostname", Hostname: publicHost, Before: cdn.Hash([]byte("initial:traffic"))}}
	return cdn.Attachment{Mode: cdn.Enabled, Owner: cdn.Owner{Target: target, App: "fixture", Incarnation: "fixture-incarnation", Attachment: "fixture-attachment"}, Binding: binding, Profile: selected, Revision: cdn.RevisionID("revision-" + string(profile)), SourceRelease: "fixture-release"}
}

func (f *Fixture) SetOrigin(path string, response Response) {
	f.mu.Lock()
	defer f.mu.Unlock()
	response.Header = response.Header.Clone()
	response.Body = append([]byte(nil), response.Body...)
	f.origin[path] = response
}

func (f *Fixture) OriginHits(path string) int { f.mu.Lock(); defer f.mu.Unlock(); return f.hits[path] }
func (f *Fixture) NativeWrites(profile cdn.ProfileRef) uint64 {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.nodes[profile].version
}
func (f *Fixture) EndpointURL(profile cdn.ProfileRef) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.nodes[profile].url
}
func (f *Fixture) Evict(profile cdn.ProfileRef) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.nodes[profile].cache = map[string]Response{}
}
func (f *Fixture) DropReply(profile cdn.ProfileRef) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.nodes[profile].drop = true
}
func (f *Fixture) DelayNext(profile cdn.ProfileRef) *Gate {
	f.mu.Lock()
	defer f.mu.Unlock()
	gate := &Gate{Started: make(chan struct{}), Release: make(chan struct{}), Finished: make(chan struct{})}
	f.nodes[profile].gate = gate
	return gate
}
func (f *Fixture) Foreign(profile cdn.ProfileRef) {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := f.nodes[profile]
	n.resource = cdn.OwnedResource{Kind: cdn.EdgeConfiguration, ID: string(profile), ExactName: string(profile), Owner: cdn.Owner{App: "foreign"}, Version: "foreign", Fingerprint: cdn.Hash([]byte("foreign"))}
	n.fingerprint = n.resource.Fingerprint
}

func (f *Fixture) serveOrigin(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	f.hits[r.URL.Path]++
	response, found := f.origin[r.URL.Path]
	f.mu.Unlock()
	if !found {
		http.NotFound(w, r)
		return
	}
	writeResponse(w, r, response)
}

func writeResponse(w http.ResponseWriter, r *http.Request, response Response) {
	for key, values := range response.Header {
		w.Header()[key] = append([]string(nil), values...)
	}
	w.WriteHeader(response.Status)
	if r.Method != http.MethodHead {
		_, _ = w.Write(response.Body)
	}
}

func (f *Fixture) fetch(r *http.Request, endpoint string) (Response, error) {
	body, err := requestBody(r)
	if err != nil {
		return Response{}, err
	}
	// #nosec G704 -- Endpoint comes only from this fixture's loopback httptest servers.
	request, err := http.NewRequestWithContext(r.Context(), r.Method, endpoint+r.URL.RequestURI(), bytes.NewReader(body))
	if err != nil {
		return Response{}, err
	}
	request.Host, request.Header = r.Host, r.Header.Clone()
	// #nosec G704 -- The request targets the fixture-owned loopback endpoint above.
	response, err := f.Client.Do(request)
	if err != nil {
		return Response{}, err
	}
	defer func() { _ = response.Body.Close() }()
	body, err = io.ReadAll(io.LimitReader(response.Body, bodyLimit+1))
	if err != nil || len(body) > bodyLimit {
		return Response{}, cdn.ErrInvalid
	}
	return Response{Status: response.StatusCode, Header: response.Header.Clone(), Body: body}, nil
}

func requestBody(r *http.Request) ([]byte, error) {
	if r.Body == nil {
		return nil, nil
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, bodyLimit+1))
	if err != nil || len(body) > bodyLimit {
		return nil, cdn.ErrInvalid
	}
	return body, nil
}

func (f *Fixture) edgeHandler(driver *Driver) func(http.ResponseWriter, *http.Request) {
	return func(w http.ResponseWriter, r *http.Request) { f.serveEdge(driver, w, r) }
}

func (f *Fixture) serveEdge(driver *Driver, w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	n := f.nodes[driver.profile.Reference]
	attachment, cached := n.attachment, n.cache[r.URL.Path]
	f.mu.Unlock()
	digest, eligible := attachment.Binding.Cache.AssetDigest(r, attachment.Binding.Hostname)
	if eligible && cached.Status == 200 {
		cached.Header = cached.Header.Clone()
		cached.Header.Set("X-CDN-Cache", "HIT")
		writeResponse(w, r, cached)
		return
	}
	response, err := f.fetch(r, f.OriginURL)
	if err != nil {
		http.Error(w, "fixture origin failed", http.StatusBadGateway)
		return
	}
	response.Header.Set("X-CDN-Endpoint", string(driver.profile.Reference))
	response.Header.Set("X-CDN-Cache", "BYPASS")
	if eligible && r.Method == http.MethodGet && cacheable(response, digest) {
		response.Header.Set("X-CDN-Cache", "MISS")
		f.mu.Lock()
		n.cache[r.URL.Path] = response
		f.mu.Unlock()
	}
	writeResponse(w, r, response)
}

func cacheable(response Response, digest cdn.Fingerprint) bool {
	typeName := strings.TrimSpace(strings.ToLower(strings.Split(response.Header.Get("Content-Type"), ";")[0]))
	if response.Status != 200 || cdn.Hash(response.Body) != digest || len(response.Header.Values("Set-Cookie")) != 0 || response.Header.Get("Content-Encoding") != "" {
		return false
	}
	if typeName == "text/html" || typeName == "application/xhtml+xml" || response.Header.Get("Vary") != "" {
		return false
	}
	return immutableDirectives(response.Header.Get("Cache-Control"))
}

func immutableDirectives(value string) bool {
	directives := map[string]bool{}
	for _, directive := range strings.Split(strings.ToLower(value), ",") {
		directives[strings.TrimSpace(directive)] = true
	}
	return directives["public"] && directives["immutable"] && directives["max-age=31536000"] && len(directives) == 3
}

func (f *Fixture) serveGateway(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	endpoint := f.OriginURL
	if f.target != "" {
		endpoint = f.nodes[f.target].url
	}
	f.mu.Unlock()
	response, err := f.fetch(r, endpoint)
	if err != nil {
		http.Error(w, "fixture traffic failed", http.StatusBadGateway)
		return
	}
	if endpoint == f.OriginURL {
		response.Header.Set("X-CDN-Endpoint", "direct")
		response.Header.Set("X-CDN-Cache", "BYPASS")
	}
	writeResponse(w, r, response)
}

func encode(w http.ResponseWriter, value any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(value)
}
