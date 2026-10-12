package spacetimedb

import (
	"context"
	"encoding/json"
	"errors"
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
type NativePublicationPreflight func() *nativePublicationPreflight

func newNativePublicationPreflight(value *nativePublicationPreflight) *NativePublicationPreflight {
	opaque := NativePublicationPreflight(func() *nativePublicationPreflight { return value })
	return &opaque
}

func (p *NativePublicationPreflight) value() *nativePublicationPreflight {
	if p == nil || *p == nil {
		return &nativePublicationPreflight{}
	}
	value := (*p)()
	if value == nil {
		return &nativePublicationPreflight{}
	}
	return value
}

type nativePublicationPreflight struct {
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

func (p *NativePublicationPreflight) ResponseBytes() []byte { return slices.Clone(p.value().response) }
func (p *NativePublicationPreflight) TokenBytes() []byte    { return []byte(p.value().token) }
func (p *NativePublicationPreflight) ResponseSHA256() string {
	return publicationBytesSHA256(p.value().response)
}
func (p *NativePublicationPreflight) TokenSHA256() string {
	return publicationBytesSHA256([]byte(p.value().token))
}
func (p *NativePublicationPreflight) Automatic() bool     { return p.value().class == "AutoMigrate" }
func (p *NativePublicationPreflight) BreaksClients() bool { return p.value().breaks }
func (p *NativePublicationPreflight) MajorUpgrade() bool  { return p.value().major }

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
	preview.value().endpoint, preview.value().wasm, preview.value().program, preview.value().response = endpoint, sha, program, raw
	preview.value().publisherSHA = publicationBytesSHA256([]byte(credential.bearer))
	preview.value().used = &atomic.Bool{}
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
	preview := newNativePublicationPreflight(&nativePublicationPreflight{class: class})
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
		preview.value().major = major
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
	preview.value().token, preview.value().breaks, preview.value().major = token, breaks, major
	return preview, nil
}

type NativePublicationReceipt func() nativePublicationReceipt

func (value NativePublicationReceipt) value() nativePublicationReceipt {
	if value == nil {
		return nativePublicationReceipt{}
	}
	return value()
}

type nativePublicationReceipt struct {
	endpoint     PrivateEndpoint
	database     DatabaseIdentity
	wasm         WasmSHA256
	program      RuntimeModuleHash
	preflightSHA string
	publisherSHA string
	response     []byte
}

func (r NativePublicationReceipt) ResponseBytes() []byte              { return slices.Clone(r.value().response) }
func (r NativePublicationReceipt) SHA256() string                     { return publicationBytesSHA256(r.value().response) }
func (r NativePublicationReceipt) WasmSHA256() WasmSHA256             { return r.value().wasm }
func (r NativePublicationReceipt) ProgramWireHash() RuntimeModuleHash { return r.value().program }

// BindingSHA256 binds the actual request as well as its response. Native success
// bodies for different candidates can have identical bytes.
func (r NativePublicationReceipt) BindingSHA256() string {
	if r.value().endpoint.base == "" || r.value().database != r.value().endpoint.database || !validHexIdentity(string(r.value().wasm)) || !validHexIdentity(string(r.value().program)) || !validHexIdentity(r.value().preflightSHA) || !validHexIdentity(r.value().publisherSHA) || len(r.value().response) == 0 {
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
	}{"native_module_update", r.value().database, r.value().wasm, r.value().program, r.value().preflightSHA, r.value().publisherSHA, r.SHA256()})
	if err != nil {
		return ""
	}
	return publicationBytesSHA256(raw)
}

func (r NativePublicationReceipt) At(endpoint PrivateEndpoint) bool {
	return r.BindingSHA256() != "" && r.value().endpoint == endpoint
}

// PublishNativeUpdate consumes one actual preflight response. BreakClients is
// fixed because the pinned Compatible policy ignores its token. Brine's owned
// stage and reviewed guest/data policy must authorize the update independently.
func PublishNativeUpdate(ctx context.Context, endpoint PrivateEndpoint, credential PublisherCredential, wasm []byte, preflight *NativePublicationPreflight) (NativePublicationReceipt, error) {
	owned := slices.Clone(wasm)
	defer clear(owned)
	sha, program, err := ModuleIdentities(owned)
	if err != nil || endpoint.base == "" || credential.bearer == "" || ctx.Err() != nil || preflight == nil || !preflight.Automatic() || preflight.value().major || preflight.value().endpoint != endpoint || preflight.value().wasm != sha || preflight.value().program != program || preflight.value().publisherSHA != publicationBytesSHA256([]byte(credential.bearer)) || preflight.value().used == nil {
		return nil, ErrNativePublicationRefused
	}
	if !preflight.value().used.CompareAndSwap(false, true) {
		return nil, ErrNativePublicationUnknown
	}
	query := url.Values{"host_type": {"Wasm"}, "policy": {"BreakClients"}, "token": {preflight.value().token}, "update_confirmation_timeout": {"5s"}}
	raw, err := nativePublicationRequest(ctx, endpoint, credential, http.MethodPut, "", query, owned)
	if err != nil {
		return nil, ErrNativePublicationUnknown
	}
	database, err := decodeNativePublicationReceipt(raw, endpoint.database)
	if err != nil {
		clear(raw)
		return nil, ErrNativePublicationUnknown
	}
	value := nativePublicationReceipt{endpoint: endpoint, database: database, wasm: sha, program: program, preflightSHA: preflight.ResponseSHA256(), publisherSHA: preflight.value().publisherSHA, response: raw}
	return NativePublicationReceipt(func() nativePublicationReceipt { return value }), nil
}

func decodeNativePublicationReceipt(raw []byte, expected DatabaseIdentity) (DatabaseIdentity, error) {
	limits := DefaultSchemaLimits()
	limits.MaxBytes = NativePublicationResponseLimit
	value, err := decodeDocument(raw, limits)
	if err != nil {
		return "", ErrNativePublicationUnknown
	}
	result, contents, err := variant(value)
	if err != nil || result != "Success" {
		return "", ErrNativePublicationUnknown
	}
	row, err := exactObject(contents, "domain", "database_identity", "op")
	if err != nil || row["domain"] != nil || row["op"] != "updated" {
		return "", ErrNativePublicationUnknown
	}
	database, valid := row["database_identity"].(string)
	if !valid || !validHexIdentity(database) || DatabaseIdentity(database) != expected {
		return "", ErrNativePublicationUnknown
	}
	return expected, nil
}

func nativePublicationRequest(ctx context.Context, endpoint PrivateEndpoint, credential PublisherCredential, method, suffix string, query url.Values, wasm []byte) ([]byte, error) {
	path := "/v1/database/" + url.PathEscape(string(endpoint.database)) + suffix
	return nativeModuleRequest(ctx, PrivateServerEndpoint{base: endpoint.base}, credential, method, path, query, wasm)
}
