package spacetimedb

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"testing"
)

func TestServingObserverNeverSendsPublisherOrRequestsOwnerEnvironment(t *testing.T) {
	fixture := &metadataFixture{schema: actualSchema(t, "schema-before.json")}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" || strings.HasSuffix(r.URL.Path, "/environment") {
			t.Error("serving used publisher authority")
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		request := r.Clone(r.Context())
		request.Header.Set("Authorization", "Bearer "+privateTestToken)
		fixture.serve(w, request)
	}))
	defer server.Close()
	address, _ := url.Parse(server.URL)
	port, _ := strconv.ParseUint(address.Port(), 10, 16)
	endpoint, err := NewPrivateEndpoint(address.Hostname(), uint16(port), databaseHex)
	if err != nil {
		t.Fatal(err)
	}
	expected, err := NewExpectedDatabase(databaseHex, publisherHex, []string{"fixture"})
	if err != nil {
		t.Fatal(err)
	}
	observed, err := ObserveServing(context.Background(), endpoint, expected, DefaultObserverLimits())
	if err != nil || observed.Database() != DatabaseIdentity(databaseHex) || observed.Publisher() != PublisherIdentity(publisherHex) || observed.Schema().SHA256() == "" {
		t.Fatal("public serving metadata refused", err)
	}
	prefix := "/v1/database/" + databaseHex
	if !slices.Equal(fixture.paths, []string{prefix, prefix + "/names", prefix + "/schema?version=10"}) {
		t.Fatal("unexpected serving requests", fixture.paths)
	}
}
