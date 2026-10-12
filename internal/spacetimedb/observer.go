package spacetimedb

import (
	"context"
	"encoding/hex"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"
)

var ErrObservation = errors.New("SpacetimeDB metadata observation refused")
var ErrPrivateCredential = errors.New("publisher credential cannot be serialized")

type DatabaseIdentity string
type PublisherIdentity string

// RuntimeModuleHash retains the native API's u256 wire identity, without 0x.
// Native Keccak-256 digest display reverses its bytes relative to this encoding.
type RuntimeModuleHash string

type PrivateEndpoint struct {
	base     string
	database DatabaseIdentity
}

type PrivateServerEndpoint struct{ base string }

// NewPrivateServerEndpoint validates location only. Its caller proves the
// delivered keys, process ownership, source scope and startup permission.
func NewPrivateServerEndpoint(host string, port uint16) (PrivateServerEndpoint, error) {
	ip := net.ParseIP(host)
	if ip == nil || !ip.IsLoopback() || port == 0 {
		return PrivateServerEndpoint{}, ErrObservation
	}
	return PrivateServerEndpoint{base: "http://" + net.JoinHostPort(ip.String(), strconv.Itoa(int(port)))}, nil
}

// NewPrivateEndpoint validates loopback location, not runtime ownership or a startup permit.
func NewPrivateEndpoint(host string, port uint16, database string) (PrivateEndpoint, error) {
	server, err := NewPrivateServerEndpoint(host, port)
	if err != nil || !validHexIdentity(database) {
		return PrivateEndpoint{}, ErrObservation
	}
	return PrivateEndpoint{base: server.base, database: DatabaseIdentity(database)}, nil
}

type PublisherCredential struct{ bearer string }

func NewPublisherCredential(token []byte) (PublisherCredential, error) {
	if len(token) == 0 || len(token) > 16<<10 || !validJWTText(string(token)) {
		return PublisherCredential{}, ErrObservation
	}
	return PublisherCredential{bearer: string(token)}, nil
}

func (PublisherCredential) String() string               { return "[private publisher credential]" }
func (PublisherCredential) GoString() string             { return "[private publisher credential]" }
func (PublisherCredential) MarshalJSON() ([]byte, error) { return nil, ErrPrivateCredential }

func validJWTText(token string) bool {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return false
	}
	for _, part := range parts {
		if part == "" || strings.IndexFunc(part, nonBase64URL) >= 0 {
			return false
		}
	}
	return true
}

func nonBase64URL(value rune) bool {
	return !(value >= 'A' && value <= 'Z' || value >= 'a' && value <= 'z' || value >= '0' && value <= '9' || value == '_' || value == '-')
}

type ExpectedDatabase struct {
	identity DatabaseIdentity
	owner    PublisherIdentity
	names    []string
	valid    bool
}

func NewExpectedDatabase(identity, owner string, names []string) (ExpectedDatabase, error) {
	if !validHexIdentity(identity) || !validHexIdentity(owner) {
		return ExpectedDatabase{}, ErrObservation
	}
	detached, err := normalizedNames(names)
	if err != nil {
		return ExpectedDatabase{}, err
	}
	return ExpectedDatabase{identity: DatabaseIdentity(identity), owner: PublisherIdentity(owner), names: detached, valid: true}, nil
}

type ObserverLimits struct {
	Timeout time.Duration
	Schema  SchemaLimits
}

func DefaultObserverLimits() ObserverLimits {
	return ObserverLimits{Timeout: 15 * time.Second, Schema: DefaultSchemaLimits()}
}

type Observation struct {
	identity DatabaseIdentity
	owner    PublisherIdentity
	names    []string
	hostType string
	module   RuntimeModuleHash
	schema   CanonicalSchema
}

func (o Observation) Database() DatabaseIdentity       { return o.identity }
func (o Observation) Publisher() PublisherIdentity     { return o.owner }
func (o Observation) Names() []string                  { return slices.Clone(o.names) }
func (o Observation) HostType() string                 { return o.hostType }
func (o Observation) CurrentModule() RuntimeModuleHash { return o.module }
func (o Observation) Schema() CanonicalSchema          { return o.schema }

func ObserveCurrent(ctx context.Context, endpoint PrivateEndpoint, credential PublisherCredential, expected ExpectedDatabase, limits ObserverLimits) (Observation, error) {
	if ctx.Err() != nil || endpoint.base == "" || endpoint.database != expected.identity || credential.bearer == "" || !expected.valid ||
		limits.Timeout <= 0 || limits.Timeout > 30*time.Second || !limits.Schema.valid() {
		return Observation{}, errors.Join(ErrObservation, ctx.Err())
	}
	ctx, cancel := context.WithTimeout(ctx, limits.Timeout)
	defer cancel()
	transport := &http.Transport{Proxy: nil, DisableKeepAlives: true, MaxResponseHeaderBytes: 64 << 10, ResponseHeaderTimeout: limits.Timeout}
	defer transport.CloseIdleConnections()
	client := http.Client{Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return ErrObservation }}
	reader := metadataReader{ctx: ctx, client: &client, endpoint: endpoint, credential: credential, limits: limits.Schema}
	observed, err := reader.observe(expected)
	if err != nil || ctx.Err() != nil {
		return Observation{}, errors.Join(ErrObservation, ctx.Err())
	}
	return observed, nil
}

type metadataReader struct {
	ctx        context.Context
	client     *http.Client
	endpoint   PrivateEndpoint
	credential PublisherCredential
	limits     SchemaLimits
}

func (r metadataReader) read(suffix string) (any, error) {
	address := r.endpoint.base + "/v1/database/" + url.PathEscape(string(r.endpoint.database)) + suffix
	request, err := http.NewRequestWithContext(r.ctx, http.MethodGet, address, nil)
	if err != nil {
		return nil, ErrObservation
	}
	request.Header.Set("Authorization", "Bearer "+r.credential.bearer)
	request.Header.Set("Accept", "application/json")
	response, err := r.client.Do(request)
	if err != nil {
		return nil, ErrObservation
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		return nil, ErrObservation
	}
	raw, err := io.ReadAll(io.LimitReader(response.Body, int64(r.limits.MaxBytes)+1))
	if err != nil || len(raw) > r.limits.MaxBytes {
		return nil, ErrObservation
	}
	return decodeDocument(raw, r.limits)
}

func (r metadataReader) observe(expected ExpectedDatabase) (Observation, error) {
	info, err := r.info()
	if err != nil || info.identity != expected.identity || info.owner != expected.owner {
		return Observation{}, ErrObservation
	}
	names, err := r.names()
	if err != nil || !slices.Equal(names, expected.names) {
		return Observation{}, ErrObservation
	}
	before, err := r.module()
	if err != nil {
		return Observation{}, err
	}
	value, err := r.read("/schema?version=10")
	if err != nil {
		return Observation{}, err
	}
	raw, err := encodeDocument(value, r.limits.MaxBytes)
	if err != nil {
		return Observation{}, err
	}
	schema, err := CanonicalSchemaV10(raw, r.limits)
	if err != nil {
		return Observation{}, err
	}
	after, err := r.module()
	if err != nil || before != after {
		return Observation{}, ErrObservation
	}
	info.names, info.module, info.schema = names, before, schema
	return info, nil
}

func (r metadataReader) info() (Observation, error) {
	value, err := r.read("")
	if err != nil {
		return Observation{}, err
	}
	row, err := exactObject(value, "database_identity", "owner_identity", "host_type", "initial_program")
	if err != nil {
		return Observation{}, err
	}
	identity, err := nativeIdentity(row["database_identity"])
	if err != nil {
		return Observation{}, err
	}
	owner, err := nativeIdentity(row["owner_identity"])
	if err != nil {
		return Observation{}, err
	}
	if _, err = runtimeHash(row["initial_program"]); err != nil {
		return Observation{}, err
	}
	hostType, unit, err := variant(row["host_type"])
	if err != nil || (hostType != "Wasm" && hostType != "Js") || emptyArray(unit) != nil {
		return Observation{}, ErrObservation
	}
	return Observation{identity: DatabaseIdentity(identity), owner: PublisherIdentity(owner), hostType: hostType}, nil
}

func (r metadataReader) names() ([]string, error) {
	value, err := r.read("/names")
	if err != nil {
		return nil, err
	}
	row, err := exactObject(value, "names")
	if err != nil {
		return nil, err
	}
	values, ok := row["names"].([]any)
	if !ok || len(values) > 128 {
		return nil, ErrObservation
	}
	names := make([]string, len(values))
	for index, value := range values {
		text, ok := value.(string)
		if !ok {
			return nil, ErrObservation
		}
		names[index] = text
	}
	return normalizedNames(names)
}

func (r metadataReader) module() (RuntimeModuleHash, error) {
	value, err := r.read("/environment")
	if err != nil {
		return "", err
	}
	row, err := exactObject(value, "module_hash", "declarations", "stored_keys")
	if err != nil {
		return "", err
	}
	declarations, declared := row["declarations"].([]any)
	keys, stored := row["stored_keys"].([]any)
	if !declared || !stored || len(declarations) > 256 || len(keys) > 256 {
		return "", ErrObservation
	}
	for _, key := range keys {
		if identifier(key) != nil {
			return "", ErrObservation
		}
	}
	return runtimeHash(row["module_hash"])
}

func nativeIdentity(value any) (string, error) {
	object, err := exactObject(value, "__identity__")
	if err != nil {
		return "", err
	}
	hash, err := runtimeHash(object["__identity__"])
	return string(hash), err
}

func runtimeHash(value any) (RuntimeModuleHash, error) {
	text, ok := value.(string)
	if !ok || !strings.HasPrefix(text, "0x") || !validHexIdentity(strings.TrimPrefix(text, "0x")) {
		return "", ErrObservation
	}
	return RuntimeModuleHash(text[2:]), nil
}

func validHexIdentity(value string) bool {
	if len(value) != 64 || strings.ToLower(value) != value {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func normalizedNames(names []string) ([]string, error) {
	if len(names) > 128 {
		return nil, ErrObservation
	}
	result := slices.Clone(names)
	for _, name := range result {
		if len(name) == 0 || len(name) > 128 || strings.ContainsAny(name, "\r\n\x00") {
			return nil, ErrObservation
		}
	}
	slices.Sort(result)
	for index := 1; index < len(result); index++ {
		if result[index-1] == result[index] {
			return nil, ErrObservation
		}
	}
	return result, nil
}
