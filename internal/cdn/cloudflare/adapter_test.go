package cloudflare

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ShaulLavo/brine/internal/cdn"
)

type apiFixture struct {
	mu         sync.Mutex
	adapter    *Adapter
	attachment cdn.Attachment
	source     []byte
	settings   scriptSettings
	exposure   exposure
	routes     []route
	record     dnsRecord
	writes     []string
	reads      []string
	loseWrite  bool
	denied     bool
	denyWrites bool
}

func profileFixture(t *testing.T) Profile {
	t.Helper()
	raw := fmt.Sprintf(`{"schema_version":1,"account_id":"%s","zone_id":"%s","worker_prefix":"brine-cdn-fixture","route_pattern":"https://game.example.test/files/*","dns_record_id":"%s","credential_ref":"operator"}`, strings.Repeat("a", 32), strings.Repeat("b", 32), strings.Repeat("d", 32))
	profile, err := ParseProfile([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	return profile
}

func newAPIFixture(t *testing.T) *apiFixture {
	t.Helper()
	p := profileFixture(t)
	edge, _ := p.Scope("edge")
	traffic, _ := p.Scope("traffic")
	target := cdn.Hash([]byte("target"))
	record := dnsRecord{ID: strings.Repeat("d", 32), Type: "A", Name: "game.example.test", Content: "192.0.2.10", TTL: 1}
	binding := cdn.Binding{Reference: "public", App: "game", Target: target, Hostname: "game.example.test",
		Origin: cdn.Origin{Address: "https://192.0.2.10", Host: "game.example.test", TLSName: "game.example.test"},
		Cache:  cdn.CachePolicy{Prefix: "/files/", Extensions: []string{"bin"}, TTLSeconds: cdn.ImmutableTTL}, AllowedEdges: []cdn.ProfileScope{edge}, Traffic: traffic,
		TrafficAuthority: cdn.TrafficAuthority{ResourceID: record.ID, Hostname: record.Name, Before: trafficFingerprint(record)}}
	a := cdn.Attachment{Mode: cdn.Enabled, Owner: cdn.Owner{Target: target, App: "game", Incarnation: "incarnation", Attachment: "attachment"}, Binding: binding, Profile: edge, Revision: "revision", SourceRelease: "release"}
	driver, err := NewEdge(p, "edge", func(context.Context, string) (string, error) { return "fixture-token", nil })
	if err != nil {
		t.Fatal(err)
	}
	fixture := &apiFixture{adapter: driver, attachment: a, routes: []route{}, record: record}
	server := httptest.NewServer(http.HandlerFunc(fixture.serve))
	t.Cleanup(server.Close)
	driver.client.base, driver.client.http = server.URL, server.Client()
	driver.client.http.CheckRedirect = noRedirect
	return fixture
}

func apiResponse(writer http.ResponseWriter, result any) {
	writer.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(writer).Encode(map[string]any{"success": true, "result": result})
}

func (f *apiFixture) serve(writer http.ResponseWriter, request *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if request.Header.Get("Authorization") != "Bearer fixture-token" {
		writer.WriteHeader(401)
		return
	}
	if f.denied {
		writer.WriteHeader(403)
		_, _ = io.WriteString(writer, "SECRET-DIAGNOSTIC")
		return
	}
	if request.Method != http.MethodGet {
		f.writes = append(f.writes, request.Method+" "+request.URL.Path)
		if f.denyWrites {
			writer.WriteHeader(403)
			_, _ = io.WriteString(writer, "SECRET-DIAGNOSTIC")
			return
		}
		f.mutate(writer, request)
		return
	}
	f.reads = append(f.reads, request.URL.Path)
	f.read(writer, request)
}

func (f *apiFixture) read(writer http.ResponseWriter, request *http.Request) {
	path := request.URL.Path
	if path == f.adapter.routePath() {
		apiResponse(writer, f.routes)
		return
	}
	if path == f.adapter.dnsPath() {
		apiResponse(writer, f.record)
		return
	}
	if len(f.source) == 0 {
		writer.WriteHeader(404)
		_, _ = io.WriteString(writer, `{"success":false}`)
		return
	}
	switch path {
	case f.adapter.scriptPath(workerName(f.adapter.client.profile, f.attachment)) + "/settings":
		apiResponse(writer, f.settings)
	case f.adapter.scriptPath(workerName(f.adapter.client.profile, f.attachment)) + "/subdomain":
		apiResponse(writer, f.exposure)
	case f.adapter.scriptPath(workerName(f.adapter.client.profile, f.attachment)) + "/content/v2":
		writer.Header().Set("Content-Type", "application/javascript+module")
		_, _ = writer.Write(f.source)
	default:
		writer.WriteHeader(404)
	}
}

func (f *apiFixture) mutate(writer http.ResponseWriter, request *http.Request) {
	name := workerName(f.adapter.client.profile, f.attachment)
	switch request.URL.Path {
	case f.adapter.scriptPath(name):
		if request.Method == http.MethodDelete {
			f.source = nil
			apiResponse(writer, nil)
			return
		}
		if err := f.upload(request); err != nil {
			writer.WriteHeader(400)
			return
		}
		if f.loseWrite {
			hijacker, _ := writer.(http.Hijacker)
			connection, _, _ := hijacker.Hijack()
			_ = connection.Close()
			return
		}
		apiResponse(writer, map[string]string{"id": name, "etag": "v1"})
	case f.adapter.scriptPath(name) + "/subdomain":
		if json.NewDecoder(request.Body).Decode(&f.exposure) != nil {
			writer.WriteHeader(400)
			return
		}
		apiResponse(writer, f.exposure)
	case f.adapter.routePath():
		var next route
		if json.NewDecoder(request.Body).Decode(&next) != nil {
			writer.WriteHeader(400)
			return
		}
		next.ID = strings.Repeat("c", 32)
		f.routes = []route{next}
		apiResponse(writer, next)
	case f.adapter.dnsPath():
		var next map[string]bool
		if json.NewDecoder(request.Body).Decode(&next) != nil || len(next) != 1 {
			writer.WriteHeader(400)
			return
		}
		proxied, ok := next["proxied"]
		if !ok {
			writer.WriteHeader(400)
			return
		}
		f.record.Proxied = proxied
		f.record.TTL = 1
		apiResponse(writer, f.record)
	default:
		if request.Method == http.MethodDelete && len(f.routes) == 1 && request.URL.Path == f.adapter.routePath()+"/"+f.routes[0].ID {
			f.routes = []route{}
			apiResponse(writer, nil)
			return
		}
		writer.WriteHeader(404)
	}
}

func (f *apiFixture) upload(request *http.Request) error {
	reader, err := request.MultipartReader()
	if err != nil {
		return err
	}
	metadata, err := reader.NextPart()
	if err != nil || metadata.FormName() != "metadata" {
		return cdn.ErrInvalid
	}
	if json.NewDecoder(metadata).Decode(&f.settings) != nil {
		return cdn.ErrInvalid
	}
	module, err := reader.NextPart()
	if err != nil || module.FormName() != "worker.mjs" {
		return cdn.ErrInvalid
	}
	f.source, err = io.ReadAll(module)
	return err
}

func fixturePermit(t *testing.T, f *apiFixture, kind cdn.ActionKind, resourceKind cdn.ResourceKind) cdn.Permit {
	t.Helper()
	observed, err := f.adapter.Observe(context.Background(), f.attachment)
	if err != nil {
		t.Fatal(err)
	}
	current := selected(observed, resourceKind)
	desired, err := f.adapter.desiredResource(f.attachment, resourceKind)
	if err != nil {
		t.Fatal(err)
	}
	return cdn.Permit{Operation: "operation", Effect: "effect", Claim: "claim", Action: cdn.Action{Kind: kind, Attachment: f.attachment, Profile: f.adapter.scope, Resource: current.Resources[0], Expected: current.Fingerprint, Desired: desired.Fingerprint}}
}

func TestNativeScriptWriteAndIndependentReadback(t *testing.T) {
	f := newAPIFixture(t)
	permit := fixturePermit(t, f, cdn.PutEdge, cdn.EdgeConfiguration)
	result, err := f.adapter.Execute(context.Background(), permit)
	if err != nil {
		t.Fatal(err)
	}
	ack, ok := result.(cdn.Acknowledged)
	if !ok || len(ack.Resources) != 1 {
		t.Fatalf("expected native acknowledgement: %#v", result)
	}
	if len(f.writes) != 1 || !strings.HasPrefix(f.writes[0], "PUT ") {
		t.Fatalf("one native write required: %v", f.writes)
	}
	if !f.settings.Observability.Enabled || !f.settings.Observability.Logs.Enabled || !f.settings.Observability.Traces.Enabled {
		t.Fatal("Worker observability absent")
	}
	inspection, err := f.adapter.Inspect(context.Background(), permit)
	if err != nil {
		t.Fatal(err)
	}
	effect, err := (cdn.Effect{Permit: permit, Stage: cdn.Issued}).Record(ack)
	if err != nil {
		t.Fatal(err)
	}
	effect, _, err = effect.Inspect(inspection)
	if err != nil || effect.Stage != cdn.Confirmed {
		t.Fatalf("independent readback failed: %v %#v", err, inspection)
	}
	for _, write := range f.writes {
		if strings.Contains(write, "/workers/subdomain") {
			t.Fatal("account namespace must never be created")
		}
	}
}

func TestUnknownNativeWriteRemainsInspectOnly(t *testing.T) {
	f := newAPIFixture(t)
	f.loseWrite = true
	permit := fixturePermit(t, f, cdn.PutEdge, cdn.EdgeConfiguration)
	result, err := f.adapter.Execute(context.Background(), permit)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := result.(cdn.Uncertain); !ok {
		t.Fatalf("lost reply must stay unknown: %#v", result)
	}
	inspection, err := f.adapter.Inspect(context.Background(), permit)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := inspection.(cdn.Observed); !ok {
		t.Fatalf("GET is not native completion proof: %#v", inspection)
	}
	effect, err := (cdn.Effect{Permit: permit, Stage: cdn.Issued}).Record(result)
	if err != nil {
		t.Fatal(err)
	}
	_, work, err := effect.Inspect(inspection)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := work.(cdn.Waiting); !ok {
		t.Fatalf("unknown effect must remain fenced: %#v", work)
	}
	_, err = f.adapter.Execute(context.Background(), permit)
	if err != nil || len(f.writes) != 1 {
		t.Fatalf("fresh exact precondition must prevent replay: %v %v", err, f.writes)
	}
}

func TestNativeOwnershipAndPermissionRefusals(t *testing.T) {
	f := newAPIFixture(t)
	f.denied = true
	_, err := f.adapter.Observe(context.Background(), f.attachment)
	api, ok := err.(*APIError)
	if !ok || api.Kind != PermissionDenied || api.Permission != ReadWorkers {
		t.Fatalf("typed permission refusal required: %v", err)
	}
	if strings.Contains(err.Error(), "SECRET") {
		t.Fatal("raw provider diagnostics escaped")
	}
	f.denied = false
	f.source = []byte("foreign")
	f.settings.Tags = []string{"foreign-owner"}
	_, err = f.adapter.Observe(context.Background(), f.attachment)
	api, ok = err.(*APIError)
	if !ok || api.Kind != OwnershipConflict || len(f.writes) != 0 {
		t.Fatalf("foreign script must be preserved: %v", err)
	}
}

func TestNativeExposureIsASeparateExplicitWrite(t *testing.T) {
	f := newAPIFixture(t)
	source, _ := WorkerSource(f.attachment)
	f.source = source
	f.settings = desiredSettings(f.attachment, source)
	f.exposure = exposure{Enabled: true, PreviewsEnabled: true}
	permit := fixturePermit(t, f, cdn.SetEdgeExposure, cdn.EdgeExposure)
	result, err := f.adapter.Execute(context.Background(), permit)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := result.(cdn.Acknowledged); !ok || len(f.writes) != 1 || !strings.Contains(f.writes[0], "/scripts/") || !strings.HasSuffix(f.writes[0], "/subdomain") || f.exposure.Enabled || f.exposure.PreviewsEnabled {
		t.Fatalf("explicit workers.dev=false write missing: %v %#v", f.writes, result)
	}
}

func TestWorkerSemanticContract(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("Node is required for native Request/Response Worker fixtures")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	output, err := exec.CommandContext(ctx, node, "--test", "--test-reporter=tap", "worker.test.mjs").CombinedOutput()
	if err != nil {
		t.Fatalf("Worker semantic fixtures: %v\n%s", err, output)
	}
	for _, line := range strings.Split(string(output), "\n") {
		if strings.HasPrefix(line, "# tests") || strings.HasPrefix(line, "# pass") {
			t.Log(line)
		}
	}
}

func TestLocalCredentialFileIsExplicitAndPrivate(t *testing.T) {
	path := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(path, []byte("fixture-token\n"), 0600); err != nil {
		t.Fatal(err)
	}
	source := FileToken("operator", path)
	value, err := source(context.Background(), "operator")
	if err != nil || value != "fixture-token" {
		t.Fatal("explicit private token unavailable")
	}
	if _, err = source(context.Background(), "other"); err != ErrCredential {
		t.Fatal("foreign credential reference admitted")
	}
	if err = os.Chmod(path, 0644); err != nil {
		t.Fatal(err)
	}
	if _, err = source(context.Background(), "operator"); err != ErrCredential {
		t.Fatal("public credential file admitted")
	}
}

func TestProviderScopeExcludesLocalCredentialSelection(t *testing.T) {
	profile := profileFixture(t)
	other := profile
	other.document.CredentialRef = "rotated_local_reference"
	if profile.Fingerprint() != other.Fingerprint() {
		t.Fatal("local credential selection changed host-authorized provider scope")
	}
	other.document.ZoneID = strings.Repeat("e", 32)
	if profile.Fingerprint() == other.Fingerprint() {
		t.Fatal("native resource authority escaped scope fingerprint")
	}
}

func TestDependentExposureSealsActualPostUploadState(t *testing.T) {
	f := newAPIFixture(t)
	before, err := f.adapter.Observe(context.Background(), f.attachment)
	if err != nil {
		t.Fatal(err)
	}
	steps, err := f.adapter.Actions(f.attachment, before)
	if err != nil || len(steps) != 3 {
		t.Fatalf("native steps: %v %#v", err, steps)
	}
	sealed, err := f.adapter.Seal(context.Background(), f.attachment, steps[0], cdn.OwnedReceipt{})
	if err != nil {
		t.Fatal(err)
	}
	if err = cdn.ValidateSealed(f.attachment, steps[0], sealed, cdn.OwnedReceipt{}); err != nil {
		t.Fatal(err)
	}
	permit := cdn.Permit{Operation: "operation", Effect: "script", Claim: "claim", Action: sealed.Action}
	result, err := f.adapter.Execute(context.Background(), permit)
	if err != nil {
		t.Fatal(err)
	}
	ack, ok := result.(cdn.Acknowledged)
	if !ok {
		t.Fatalf("script acknowledgement required: %#v", result)
	}
	prior := cdn.OwnedReceipt{Effect: permit.Effect, Profile: f.adapter.scope, Resources: ack.Resources}
	next, err := f.adapter.Seal(context.Background(), f.attachment, steps[1], prior)
	if err != nil {
		t.Fatal(err)
	}
	observed, err := f.adapter.Observe(context.Background(), f.attachment)
	if err != nil {
		t.Fatal(err)
	}
	next.Prerequisites = []cdn.Observation{observed}
	if err = cdn.ValidateSealed(f.attachment, steps[1], next, prior); err != nil {
		t.Fatal(err)
	}
	if next.Action.Expected != next.Action.Desired || next.Action.Resource.ID == "" {
		t.Fatal("fresh post-upload disabled exposure should be an unissued no-op, not guessed absent")
	}
	if len(f.writes) != 1 {
		t.Fatal("sealing must be read-only")
	}
}

func TestTrafficAdapterOnlyMutatesItsExplicitRecord(t *testing.T) {
	f := newAPIFixture(t)
	traffic, err := NewTraffic(f.adapter.client.profile, "traffic", f.adapter.client.tokens)
	if err != nil {
		t.Fatal(err)
	}
	traffic.client.http, traffic.client.base = f.adapter.client.http, f.adapter.client.base
	f.adapter = traffic
	before, err := traffic.Observe(context.Background(), f.attachment)
	if err != nil {
		t.Fatal(err)
	}
	steps, err := traffic.Actions(f.attachment, before)
	if err != nil || len(steps) != 1 {
		t.Fatalf("traffic step: %v %#v", err, steps)
	}
	sealed, err := traffic.Seal(context.Background(), f.attachment, steps[0], cdn.OwnedReceipt{})
	if err != nil {
		t.Fatal(err)
	}
	permit := cdn.Permit{Operation: "operation", Effect: "traffic", Claim: "claim", Action: sealed.Action}
	result, err := traffic.Execute(context.Background(), permit)
	if err != nil {
		t.Fatal(err)
	}
	ack, ok := result.(cdn.Acknowledged)
	if !ok {
		t.Fatalf("DNS acknowledgement required: %#v", result)
	}
	inspection, err := traffic.Inspect(context.Background(), permit)
	if err != nil {
		t.Fatal(err)
	}
	effect, err := (cdn.Effect{Permit: permit, Stage: cdn.Issued}).Record(ack)
	if err != nil {
		t.Fatal(err)
	}
	effect, _, err = effect.Inspect(inspection)
	if err != nil || effect.Stage != cdn.Confirmed {
		t.Fatalf("DNS readback: %v", err)
	}
	if len(f.writes) != 1 || f.writes[0] != "PATCH "+traffic.dnsPath() {
		t.Fatalf("only exact DNS patch allowed: %v", f.writes)
	}
	for _, path := range f.reads {
		if strings.Contains(path, "workers") {
			t.Fatalf("DNS adapter read edge authority: %s", path)
		}
	}
}

func TestForeignRouteAndRecordAreNotAdopted(t *testing.T) {
	f := newAPIFixture(t)
	f.routes = []route{{ID: strings.Repeat("c", 32), Pattern: f.adapter.client.profile.document.RoutePattern, Script: "foreign"}}
	if _, err := f.adapter.Observe(context.Background(), f.attachment); err == nil {
		t.Fatal("foreign route was adopted")
	}
	traffic, _ := NewTraffic(f.adapter.client.profile, "traffic", f.adapter.client.tokens)
	traffic.client.http, traffic.client.base = f.adapter.client.http, f.adapter.client.base
	f.adapter = traffic
	f.record.Name = "other.example.test"
	if _, err := traffic.Observe(context.Background(), f.attachment); err == nil {
		t.Fatal("foreign DNS name was adopted")
	}
	if len(f.writes) != 0 {
		t.Fatalf("refusal wrote resources: %v", f.writes)
	}
}

func TestNativeModuleReadbackIsBoundedAndUnambiguous(t *testing.T) {
	f := newAPIFixture(t)
	body, contentType, err := scriptUpload(f.attachment)
	if err != nil {
		t.Fatal(err)
	}
	actual, err := moduleSource(reply{body: body, headers: http.Header{"Content-Type": []string{contentType}}})
	if err != nil {
		t.Fatal(err)
	}
	expected, _ := WorkerSource(f.attachment)
	if !bytes.Equal(actual, expected) {
		t.Fatal("multipart readback changed module bytes")
	}
	var duplicate bytes.Buffer
	writer := multipart.NewWriter(&duplicate)
	if err = uploadPart(writer, "worker.mjs", "application/javascript+module", expected); err != nil {
		t.Fatal(err)
	}
	if err = uploadPart(writer, "worker.mjs", "application/javascript+module", []byte("foreign")); err != nil {
		t.Fatal(err)
	}
	if err = writer.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err = moduleSource(reply{body: duplicate.Bytes(), headers: http.Header{"Content-Type": []string{writer.FormDataContentType()}}}); err == nil {
		t.Fatal("ambiguous duplicate module was accepted")
	}
}

func TestNativeRouteCreateAndDisableHaveIndependentReadback(t *testing.T) {
	f := newAPIFixture(t)
	source, _ := WorkerSource(f.attachment)
	f.source = source
	f.settings = desiredSettings(f.attachment, source)
	for _, mode := range []cdn.Mode{cdn.Enabled, cdn.Direct} {
		f.attachment.Mode = mode
		kind := cdn.PutEdgeRoute
		if mode == cdn.Direct {
			kind = cdn.RemoveEdgeRoute
		}
		permit := fixturePermit(t, f, kind, cdn.EdgeRoute)
		result, err := f.adapter.Execute(context.Background(), permit)
		if err != nil {
			t.Fatal(err)
		}
		ack, ok := result.(cdn.Acknowledged)
		if !ok {
			t.Fatalf("route native acknowledgement: %#v", result)
		}
		inspection, err := f.adapter.Inspect(context.Background(), permit)
		if err != nil {
			t.Fatal(err)
		}
		effect, err := (cdn.Effect{Permit: permit, Stage: cdn.Issued}).Record(ack)
		if err != nil {
			t.Fatal(err)
		}
		effect, _, err = effect.Inspect(inspection)
		if err != nil || effect.Stage != cdn.Confirmed {
			t.Fatalf("route independent readback: %v", err)
		}
	}
	if len(f.writes) != 2 || !strings.HasPrefix(f.writes[0], "POST ") || !strings.HasPrefix(f.writes[1], "DELETE ") {
		t.Fatalf("one write per route action required: %v", f.writes)
	}
}

func TestNativeWritePermissionFailureIsKnownRefusal(t *testing.T) {
	f := newAPIFixture(t)
	permit := fixturePermit(t, f, cdn.PutEdge, cdn.EdgeConfiguration)
	f.denyWrites = true
	result, err := f.adapter.Execute(context.Background(), permit)
	if err != nil {
		t.Fatal(err)
	}
	refused, ok := result.(cdn.Refused)
	if !ok || refused.Code != string(PermissionDenied)+":"+string(WriteWorkers) {
		t.Fatalf("typed write permission refusal: %#v", result)
	}
	if len(f.writes) != 1 || len(f.source) != 0 {
		t.Fatal("refused native write changed provider state")
	}
}
