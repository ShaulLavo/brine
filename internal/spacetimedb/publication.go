package spacetimedb

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"slices"
	"sync/atomic"
)

const NativePublicationResponseLimit = 64 << 10

var ErrNativePreflight = errors.New("private native publication preflight refused")
var ErrNativePublicationRefused = errors.New("private native publication refused before effect")
var ErrNativePublicationUnknown = errors.New("private native publication requires inspection")

// NativePublicationPreflight owns an actual bounded private API response. It
// establishes native token provenance only, not Brine review or host authority.
type NativePublicationPreflight struct {
	endpoint     PrivateEndpoint
	wasm         WasmSHA256
	program      RuntimeModuleHash
	publisherSHA string
	response     []byte
	token        string
	class        string
	breaks       bool
	major        bool
	used         *atomic.Bool
}

func (p *NativePublicationPreflight) ResponseBytes() []byte { return slices.Clone(p.response) }
func (p *NativePublicationPreflight) TokenBytes() []byte    { return []byte(p.token) }
func (p *NativePublicationPreflight) ResponseSHA256() string {
	return publicationBytesSHA256(p.response)
}
func (p *NativePublicationPreflight) TokenSHA256() string {
	return publicationBytesSHA256([]byte(p.token))
}
func (p *NativePublicationPreflight) Automatic() bool     { return p.class == "AutoMigrate" }
func (p *NativePublicationPreflight) BreaksClients() bool { return p.breaks }
func (p *NativePublicationPreflight) MajorUpgrade() bool  { return p.major }
func (NativePublicationPreflight) String() string         { return "[private native publication preflight]" }
func (NativePublicationPreflight) GoString() string       { return "[private native publication preflight]" }
func (NativePublicationPreflight) MarshalJSON() ([]byte, error) {
	return nil, ErrPrivateCredential
}

// PreflightNativePublication uses only the private loopback endpoint and fixed
// Wasm/NoColor purpose. Host ownership and exact source checks precede this call.
func PreflightNativePublication(ctx context.Context, endpoint PrivateEndpoint, credential PublisherCredential, wasm []byte) (*NativePublicationPreflight, error) {
	owned := slices.Clone(wasm)
	defer clear(owned)
	sha, program, err := ModuleIdentities(owned)
	if err != nil || endpoint.base == "" || credential.bearer == "" || ctx.Err() != nil {
		return nil, ErrNativePreflight
	}
	query := url.Values{"host_type": {"Wasm"}, "style": {"NoColor"}}
	raw, err := nativePublicationRequest(ctx, endpoint, credential, http.MethodPost, "/pre_publish", query, owned)
	if err != nil {
		return nil, ErrNativePreflight
	}
	preview, err := decodeNativePublicationPreflight(raw)
	if err != nil {
		clear(raw)
		return nil, ErrNativePreflight
	}
	preview.endpoint, preview.wasm, preview.program, preview.response = endpoint, sha, program, raw
	preview.publisherSHA = publicationBytesSHA256([]byte(credential.bearer))
	preview.used = &atomic.Bool{}
	return preview, nil
}

func decodeNativePublicationPreflight(raw []byte) (*NativePublicationPreflight, error) {
	limits := DefaultSchemaLimits()
	limits.MaxBytes = NativePublicationResponseLimit
	value, err := decodeDocument(raw, limits)
	if err != nil {
		return nil, ErrNativePreflight
	}
	class, contents, err := variant(value)
	if err != nil {
		return nil, ErrNativePreflight
	}
	preview := &NativePublicationPreflight{class: class}
	if class == "ManualMigrate" {
		row, err := exactObject(contents, "reason", "major_version_upgrade")
		if err != nil {
			return nil, ErrNativePreflight
		}
		reason, valid := row["reason"].(string)
		major, majorValid := row["major_version_upgrade"].(bool)
		if !valid || reason == "" || !majorValid {
			return nil, ErrNativePreflight
		}
		preview.major = major
		return preview, nil
	}
	if class != "AutoMigrate" {
		return nil, ErrNativePreflight
	}
	row, err := exactObject(contents, "migrate_plan", "break_clients", "token", "major_version_upgrade")
	if err != nil {
		return nil, ErrNativePreflight
	}
	plan, planValid := row["migrate_plan"].(string)
	token, tokenValid := row["token"].(string)
	breaks, breaksValid := row["break_clients"].(bool)
	major, majorValid := row["major_version_upgrade"].(bool)
	if _, err = runtimeHash(token); err != nil || !planValid || plan == "" || !tokenValid || !breaksValid || !majorValid {
		return nil, ErrNativePreflight
	}
	preview.token, preview.breaks, preview.major = token, breaks, major
	return preview, nil
}

type NativePublicationReceipt struct {
	endpoint     PrivateEndpoint
	database     DatabaseIdentity
	wasm         WasmSHA256
	program      RuntimeModuleHash
	preflightSHA string
	publisherSHA string
	response     []byte
}

func (r NativePublicationReceipt) Database() DatabaseIdentity         { return r.database }
func (r NativePublicationReceipt) ResponseBytes() []byte              { return slices.Clone(r.response) }
func (r NativePublicationReceipt) SHA256() string                     { return publicationBytesSHA256(r.response) }
func (r NativePublicationReceipt) WasmSHA256() WasmSHA256             { return r.wasm }
func (r NativePublicationReceipt) ProgramWireHash() RuntimeModuleHash { return r.program }
func (r NativePublicationReceipt) PreflightResponseSHA256() string    { return r.preflightSHA }
func (r NativePublicationReceipt) PublisherCredentialSHA256() string  { return r.publisherSHA }

// BindingSHA256 binds the actual request as well as its response. Native success
// bodies for different candidates can have identical bytes.
func (r NativePublicationReceipt) BindingSHA256() string {
	if r.endpoint.base == "" || r.database != r.endpoint.database || !validHexIdentity(string(r.wasm)) || !validHexIdentity(string(r.program)) || !validHexIdentity(r.preflightSHA) || !validHexIdentity(r.publisherSHA) || len(r.response) == 0 {
		return ""
	}
	raw, err := json.Marshal(struct {
		Purpose      string            `json:"purpose"`
		Database     DatabaseIdentity  `json:"database"`
		Wasm         WasmSHA256        `json:"wasm_sha256"`
		Program      RuntimeModuleHash `json:"program_wire_hash"`
		PreflightSHA string            `json:"preflight_response_sha256"`
		PublisherSHA string            `json:"publisher_credential_sha256"`
		ResponseSHA  string            `json:"response_sha256"`
	}{"native_module_update", r.database, r.wasm, r.program, r.preflightSHA, r.publisherSHA, r.SHA256()})
	if err != nil {
		return ""
	}
	return publicationBytesSHA256(raw)
}

func (r NativePublicationReceipt) At(endpoint PrivateEndpoint) bool {
	return r.BindingSHA256() != "" && r.endpoint == endpoint
}
func (NativePublicationReceipt) String() string               { return "[private native publication receipt]" }
func (NativePublicationReceipt) GoString() string             { return "[private native publication receipt]" }
func (NativePublicationReceipt) MarshalJSON() ([]byte, error) { return nil, ErrPrivateCredential }

// PublishNativeUpdate consumes one actual preflight response. BreakClients is
// fixed because the pinned Compatible policy ignores its token. Brine's owned
// stage and reviewed guest/data policy must authorize the update independently.
func PublishNativeUpdate(ctx context.Context, endpoint PrivateEndpoint, credential PublisherCredential, wasm []byte, preflight *NativePublicationPreflight) (NativePublicationReceipt, error) {
	owned := slices.Clone(wasm)
	defer clear(owned)
	sha, program, err := ModuleIdentities(owned)
	if err != nil || endpoint.base == "" || credential.bearer == "" || ctx.Err() != nil || preflight == nil || !preflight.Automatic() || preflight.major || preflight.endpoint != endpoint || preflight.wasm != sha || preflight.program != program || preflight.publisherSHA != publicationBytesSHA256([]byte(credential.bearer)) || preflight.used == nil {
		return NativePublicationReceipt{}, ErrNativePublicationRefused
	}
	if !preflight.used.CompareAndSwap(false, true) {
		return NativePublicationReceipt{}, ErrNativePublicationUnknown
	}
	query := url.Values{"host_type": {"Wasm"}, "policy": {"BreakClients"}, "token": {preflight.token}, "update_confirmation_timeout": {"5s"}}
	raw, err := nativePublicationRequest(ctx, endpoint, credential, http.MethodPut, "", query, owned)
	if err != nil {
		return NativePublicationReceipt{}, ErrNativePublicationUnknown
	}
	receipt, err := decodeNativePublicationReceipt(raw, endpoint.database)
	if err != nil {
		clear(raw)
		return NativePublicationReceipt{}, ErrNativePublicationUnknown
	}
	receipt.endpoint, receipt.wasm, receipt.program = endpoint, sha, program
	receipt.preflightSHA, receipt.publisherSHA, receipt.response = preflight.ResponseSHA256(), preflight.publisherSHA, raw
	return receipt, nil
}

func decodeNativePublicationReceipt(raw []byte, expected DatabaseIdentity) (NativePublicationReceipt, error) {
	limits := DefaultSchemaLimits()
	limits.MaxBytes = NativePublicationResponseLimit
	value, err := decodeDocument(raw, limits)
	if err != nil {
		return NativePublicationReceipt{}, ErrNativePublicationUnknown
	}
	result, contents, err := variant(value)
	if err != nil || result != "Success" {
		return NativePublicationReceipt{}, ErrNativePublicationUnknown
	}
	row, err := exactObject(contents, "domain", "database_identity", "op")
	if err != nil || row["domain"] != nil || row["op"] != "updated" {
		return NativePublicationReceipt{}, ErrNativePublicationUnknown
	}
	database, valid := row["database_identity"].(string)
	if !valid || !validHexIdentity(database) || DatabaseIdentity(database) != expected {
		return NativePublicationReceipt{}, ErrNativePublicationUnknown
	}
	return NativePublicationReceipt{database: expected}, nil
}

func nativePublicationRequest(ctx context.Context, endpoint PrivateEndpoint, credential PublisherCredential, method, suffix string, query url.Values, wasm []byte) ([]byte, error) {
	wait, cancel := context.WithTimeout(ctx, nativePublicationWait)
	defer cancel()
	transport := &http.Transport{Proxy: nil, DisableKeepAlives: true, MaxResponseHeaderBytes: 64 << 10, ResponseHeaderTimeout: nativePublicationWait}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return ErrNativePreflight }}
	address := endpoint.base + "/v1/database/" + url.PathEscape(string(endpoint.database)) + suffix + "?" + query.Encode()
	request, err := http.NewRequestWithContext(wait, method, address, bytes.NewReader(wasm))
	if err != nil {
		return nil, ErrNativePreflight
	}
	request.GetBody = nil
	request.Close = true
	request.Header.Set("Authorization", "Bearer "+credential.bearer)
	request.Header.Set("Content-Type", "application/octet-stream")
	request.Header.Set("Accept", "application/json")
	response, err := client.Do(request)
	if err != nil {
		return nil, ErrNativePreflight
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		return nil, ErrNativePreflight
	}
	raw, err := io.ReadAll(io.LimitReader(response.Body, NativePublicationResponseLimit+1))
	if err != nil || len(raw) > NativePublicationResponseLimit || wait.Err() != nil {
		clear(raw)
		return nil, ErrNativePreflight
	}
	return raw, nil
}
