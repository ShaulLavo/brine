package spacetimedb

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

const databaseHex = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
const publisherHex = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
const initialHex = "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"
const currentHex = "594a40f8dc12577e4d185ad74d8a61d3894015a3f356a2f727214ef40024f072"
const privateTestToken = "fake-header.fake-payload.fake-signature"

type metadataFixture struct {
	mu           sync.Mutex
	paths        []string
	environments int
	mode         string
	schema       []byte
}

func (f *metadataFixture) serve(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.paths = append(f.paths, r.URL.RequestURI())
	if r.Method != http.MethodGet || r.Header.Get("Authorization") != "Bearer "+privateTestToken {
		w.WriteHeader(http.StatusForbidden)
		return
	}
	if f.mode == "deny" {
		http.Error(w, privateTestToken, http.StatusForbidden)
		return
	}
	if f.mode == "redirect" {
		w.Header().Set("Location", "http://127.0.0.1:1/private?token="+privateTestToken)
		w.WriteHeader(http.StatusFound)
		return
	}
	if f.mode == "oversize" {
		_, _ = fmt.Fprint(w, strings.Repeat("x", (1<<20)+1))
		return
	}
	w.Header().Set("Spacetime-Identity-Token", privateTestToken)
	path := strings.TrimPrefix(r.URL.Path, "/v1/database/"+databaseHex)
	switch path {
	case "":
		f.writeInfo(w)
	case "/names":
		_, _ = fmt.Fprint(w, `{"names":["fixture"]}`)
	case "/environment":
		f.environments++
		f.writeEnvironment(w)
	case "/schema":
		if r.URL.RawQuery != "version=10" {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		_, _ = w.Write(f.schema)
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

func (f *metadataFixture) writeInfo(w http.ResponseWriter) {
	owner := publisherHex
	if f.mode == "wrong-owner" {
		owner = initialHex
	}
	object := map[string]any{"database_identity": map[string]string{"__identity__": "0x" + databaseHex},
		"owner_identity": map[string]string{"__identity__": "0x" + owner}, "host_type": map[string]any{"Wasm": []any{}}, "initial_program": "0x" + initialHex}
	if f.mode == "extra-field" {
		object["future"] = privateTestToken
	}
	_ = json.NewEncoder(w).Encode(object)
}

func (f *metadataFixture) writeEnvironment(w http.ResponseWriter) {
	module := currentHex
	if f.mode == "module-race" && f.environments == 2 {
		module = initialHex
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"module_hash": "0x" + module, "declarations": []any{}, "stored_keys": []string{"PRIVATE_NAME"}})
}

func observationFixture(t *testing.T, mode string) (PrivateEndpoint, PublisherCredential, ExpectedDatabase, *metadataFixture) {
	t.Helper()
	f := &metadataFixture{mode: mode, schema: actualSchema(t, "schema-before.json")}
	server := httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(server.Close)
	address, _ := url.Parse(server.URL)
	port, _ := strconv.ParseUint(address.Port(), 10, 16)
	endpoint, err := NewPrivateEndpoint(address.Hostname(), uint16(port), databaseHex)
	if err != nil {
		t.Fatal(err)
	}
	credential, err := NewPublisherCredential([]byte(privateTestToken))
	if err != nil {
		t.Fatal(err)
	}
	expected, err := NewExpectedDatabase(databaseHex, publisherHex, []string{"fixture"})
	if err != nil {
		t.Fatal(err)
	}
	return endpoint, credential, expected, f
}

func TestFixedPrivateObserverUsesCurrentHashAndNoHooks(t *testing.T) {
	endpoint, credential, expected, fixture := observationFixture(t, "")
	observed, err := ObserveCurrent(context.Background(), endpoint, credential, expected, DefaultObserverLimits())
	if err != nil {
		t.Fatal(err)
	}
	prefix := "/v1/database/" + databaseHex
	want := []string{prefix, prefix + "/names", prefix + "/environment", prefix + "/schema?version=10", prefix + "/environment"}
	if !slices.Equal(fixture.paths, want) {
		t.Fatal("observer issued an unqualified route", fixture.paths)
	}
	if string(observed.CurrentModule()) != currentHex || observed.Database() != DatabaseIdentity(databaseHex) || observed.Publisher() != PublisherIdentity(publisherHex) {
		t.Fatal("initial_program substituted for current module")
	}
	if observed.Schema().SHA256() == "" || observed.HostType() != "Wasm" {
		t.Fatal("missing authoritative schema")
	}
	names := observed.Names()
	names[0] = "changed"
	if observed.Names()[0] != "fixture" {
		t.Fatal("caller mutated the observation")
	}
	diagnostic := fmt.Sprintf("%+v", observed)
	if strings.Contains(diagnostic, privateTestToken) || strings.Contains(diagnostic, "PRIVATE_NAME") {
		t.Fatal("private HTTP metadata reached public observation")
	}
}

func TestObserverRefusesDriftUnknownMetadataAndSensitiveErrors(t *testing.T) {
	for _, mode := range []string{"wrong-owner", "module-race", "extra-field", "deny", "redirect", "oversize"} {
		t.Run(mode, func(t *testing.T) {
			assertObservationRefused(t, mode)
		})
	}
}

func TestFixedObserverAcceptsNativeDefaultBytes(t *testing.T) {
	endpoint, credential, expected, fixture := observationFixture(t, "")
	fixture.schema = actualSchema(t, "schema-default-column.json")
	observed, err := ObserveCurrent(context.Background(), endpoint, credential, expected, DefaultObserverLimits())
	if err != nil {
		t.Fatal("native default-column GET schema refused", err)
	}
	canonical, err := CanonicalSchemaV10(fixture.schema, DefaultSchemaLimits())
	if err != nil || observed.Schema().SHA256() != canonical.SHA256() {
		t.Fatal("observer changed native default schema", err)
	}
}

func assertObservationRefused(t *testing.T, mode string) {
	t.Helper()
	endpoint, credential, expected, _ := observationFixture(t, mode)
	observed, err := ObserveCurrent(context.Background(), endpoint, credential, expected, DefaultObserverLimits())
	if !errors.Is(err, ErrObservation) || observed.CurrentModule() != "" || strings.Contains(err.Error(), privateTestToken) || strings.Contains(err.Error(), "http") {
		t.Fatal("failed observation issued metadata or unsanitized error", observed.CurrentModule(), err)
	}
}

func TestObserverDeadlineAndInvalidAuthorityDoNotRead(t *testing.T) {
	endpoint, credential, expected, fixture := observationFixture(t, "")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := ObserveCurrent(ctx, endpoint, credential, expected, DefaultObserverLimits())
	if !errors.Is(err, context.Canceled) || len(fixture.paths) != 0 {
		t.Fatal("cancellation crossed observation boundary", err)
	}
	limits := DefaultObserverLimits()
	limits.Timeout = 31 * time.Second
	_, err = ObserveCurrent(context.Background(), endpoint, credential, expected, limits)
	if err == nil || len(fixture.paths) != 0 {
		t.Fatal("unbounded observer accepted")
	}
	wrong, _ := NewExpectedDatabase(databaseHex, publisherHex, []string{"other"})
	if _, err = ObserveCurrent(context.Background(), endpoint, credential, wrong, DefaultObserverLimits()); err == nil {
		t.Fatal("logical name drift accepted")
	}
}

func TestPrivateLocationsAndCredentialSerialization(t *testing.T) {
	for _, host := range []string{"localhost", "example.test", "192.0.2.1", "127.0.0.1/path"} {
		if _, err := NewPrivateEndpoint(host, 3000, databaseHex); err == nil {
			t.Fatal("non-literal-loopback endpoint accepted", host)
		}
	}
	if _, err := NewPrivateEndpoint("127.0.0.1", 0, databaseHex); err == nil {
		t.Fatal("unreserved endpoint accepted")
	}
	for _, token := range []string{"", "Bearer " + privateTestToken, privateTestToken + "\r\nInjected: true", "bad", "a..c"} {
		if _, err := NewPublisherCredential([]byte(token)); err == nil {
			t.Fatal("invalid credential accepted")
		}
	}
	input := []byte(privateTestToken)
	credential, _ := NewPublisherCredential(input)
	input[0] = 'x'
	if credential.bearer != privateTestToken {
		t.Fatal("credential retained mutable input")
	}
	if _, err := json.Marshal(credential); !errors.Is(err, ErrPrivateCredential) {
		t.Fatal("private credential serialized", err)
	}
	if strings.Contains(fmt.Sprintf("%v %+v %#v", credential, credential, credential), privateTestToken) {
		t.Fatal("formatter disclosed publisher credential")
	}
	names := []string{"fixture"}
	expected, _ := NewExpectedDatabase(databaseHex, publisherHex, names)
	names[0] = "changed"
	if expected.names[0] != "fixture" {
		t.Fatal("expected binding retained mutable names")
	}
}
