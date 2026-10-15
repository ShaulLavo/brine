package spacetimedb

import (
	"context"
	"net/http"
	"slices"
	"time"
)

// ServingObservation contains public metadata, not owner-only module authority.
// Publication separately verifies the current program through /environment.
type ServingObservation struct{ metadata Observation }

func (o ServingObservation) Database() DatabaseIdentity   { return o.metadata.Database() }
func (o ServingObservation) Publisher() PublisherIdentity { return o.metadata.Publisher() }
func (o ServingObservation) Names() []string              { return o.metadata.Names() }
func (o ServingObservation) Schema() CanonicalSchema      { return o.metadata.Schema() }

func ObserveServing(ctx context.Context, endpoint PrivateEndpoint, expected ExpectedDatabase, limits ObserverLimits) (ServingObservation, error) {
	if ctx.Err() != nil || endpoint.base == "" || endpoint.database != expected.identity || !expected.valid || limits.Timeout <= 0 || limits.Timeout > 30*time.Second || !limits.Schema.valid() {
		return ServingObservation{}, ErrObservation
	}
	ctx, cancel := context.WithTimeout(ctx, limits.Timeout)
	defer cancel()
	transport := &http.Transport{Proxy: nil, DisableKeepAlives: true, MaxResponseHeaderBytes: 64 << 10, ResponseHeaderTimeout: limits.Timeout}
	defer transport.CloseIdleConnections()
	client := http.Client{Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return ErrObservation }}
	reader := metadataReader{ctx: ctx, client: &client, endpoint: endpoint, limits: limits.Schema}
	info, err := reader.info()
	if err != nil || info.identity != expected.identity || info.owner != expected.owner {
		return ServingObservation{}, ErrObservation
	}
	names, err := reader.names()
	if err != nil || !slices.Equal(names, expected.names) {
		return ServingObservation{}, ErrObservation
	}
	value, err := reader.read("/schema?version=10")
	if err != nil {
		return ServingObservation{}, ErrObservation
	}
	raw, err := encodeDocument(value, limits.Schema.MaxBytes)
	if err != nil {
		return ServingObservation{}, ErrObservation
	}
	schema, err := CanonicalSchemaV10(raw, limits.Schema)
	if err != nil || ctx.Err() != nil {
		return ServingObservation{}, ErrObservation
	}
	info.names, info.schema = names, schema
	return ServingObservation{metadata: info}, nil
}
