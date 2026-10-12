package spacetimedb

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"slices"
)

// NativeCreateReceipt records the actual initial publication request and native
// response. It grants no host, empty-stage, promotion or data authority.
type NativeCreateReceipt struct {
	endpoint        PrivateServerEndpoint
	database        DatabaseIdentity
	publisher       PublisherIdentity
	name            string
	wasm            WasmSHA256
	program         RuntimeModuleHash
	publisherSHA    string
	verificationSHA string
	response        []byte
}

func (r NativeCreateReceipt) Database() DatabaseIdentity         { return r.database }
func (r NativeCreateReceipt) Publisher() PublisherIdentity       { return r.publisher }
func (r NativeCreateReceipt) Name() string                       { return r.name }
func (r NativeCreateReceipt) WasmSHA256() WasmSHA256             { return r.wasm }
func (r NativeCreateReceipt) ProgramWireHash() RuntimeModuleHash { return r.program }
func (r NativeCreateReceipt) PublisherCredentialSHA256() string  { return r.publisherSHA }
func (r NativeCreateReceipt) VerificationSHA256() string         { return r.verificationSHA }
func (r NativeCreateReceipt) ResponseBytes() []byte              { return slices.Clone(r.response) }
func (r NativeCreateReceipt) SHA256() string {
	if len(r.response) == 0 {
		return ""
	}
	return publicationBytesSHA256(r.response)
}

func (r NativeCreateReceipt) BindingSHA256() string {
	if r.endpoint.base == "" || !validHexIdentity(string(r.database)) || !validHexIdentity(string(r.publisher)) || !validNativeCreationName(r.name) || !validHexIdentity(string(r.wasm)) || !validHexIdentity(string(r.program)) || !validHexIdentity(r.publisherSHA) || !validHexIdentity(r.verificationSHA) || len(r.response) == 0 {
		return ""
	}
	raw, err := json.Marshal(struct {
		Purpose         string            `json:"purpose"`
		EndpointSHA     string            `json:"endpoint_sha256"`
		Database        DatabaseIdentity  `json:"database"`
		Publisher       PublisherIdentity `json:"publisher"`
		Name            string            `json:"name"`
		Wasm            WasmSHA256        `json:"wasm_sha256"`
		Program         RuntimeModuleHash `json:"program_wire_hash"`
		PublisherSHA    string            `json:"publisher_credential_sha256"`
		VerificationSHA string            `json:"verification_sha256"`
		ResponseSHA     string            `json:"response_sha256"`
	}{"native_module_create", r.endpoint.SHA256(), r.database, r.publisher, r.name, r.wasm, r.program, r.publisherSHA, r.verificationSHA, r.SHA256()})
	if err != nil {
		return ""
	}
	return publicationBytesSHA256(raw)
}

func (r NativeCreateReceipt) At(endpoint PrivateServerEndpoint) bool {
	return r.endpoint == endpoint && r.BindingSHA256() != ""
}
func (NativeCreateReceipt) String() string               { return "[private native create receipt]" }
func (NativeCreateReceipt) GoString() string             { return "[private native create receipt]" }
func (NativeCreateReceipt) MarshalJSON() ([]byte, error) { return nil, ErrPrivateCredential }

// PublishNativeCreate consumes one actual supplied-publisher verification for a
// fixed named Wasm publication. The caller owns the empty private stage, namespace
// and exclusive writer lease. Native PUT can update an existing database, so an
// unexpected operation or any uncertain response requires inspection, not retry.
func PublishNativeCreate(ctx context.Context, endpoint PrivateServerEndpoint, credential PublisherCredential, verification NativePublisherVerification, name string, wasm []byte) (NativeCreateReceipt, error) {
	if ctx.Err() != nil || !verification.At(endpoint) || verification.createUsed == nil || verification.CredentialSHA256() != publicationBytesSHA256([]byte(credential.bearer)) || !validNativeCreationName(name) || len(wasm) > MaxWasmBytes {
		return NativeCreateReceipt{}, ErrNativePublicationRefused
	}
	owned := slices.Clone(wasm)
	defer clear(owned)
	sha, program, err := ModuleIdentities(owned)
	if err != nil {
		return NativeCreateReceipt{}, ErrNativePublicationRefused
	}
	if !verification.createUsed.CompareAndSwap(false, true) {
		return NativeCreateReceipt{}, ErrNativePublicationUnknown
	}
	query := url.Values{"host_type": {"Wasm"}, "policy": {"Compatible"}, "num_replicas": {"1"}}
	raw, err := nativeModuleRequest(ctx, endpoint, credential, http.MethodPut, "/v1/database/"+url.PathEscape(name), query, owned)
	if err != nil {
		return NativeCreateReceipt{}, ErrNativePublicationUnknown
	}
	database, err := decodeNativeCreateReceipt(raw, name)
	if err != nil {
		clear(raw)
		return NativeCreateReceipt{}, ErrNativePublicationUnknown
	}
	return NativeCreateReceipt{endpoint: endpoint, database: database, publisher: verification.Publisher(), name: name,
		wasm: sha, program: program, publisherSHA: verification.CredentialSHA256(), verificationSHA: verification.SHA256(), response: raw}, nil
}

func decodeNativeCreateReceipt(raw []byte, name string) (DatabaseIdentity, error) {
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
	if err != nil || row["domain"] != name || row["op"] != "created" {
		return "", ErrNativePublicationUnknown
	}
	database, valid := row["database_identity"].(string)
	if !valid || !validHexIdentity(database) {
		return "", ErrNativePublicationUnknown
	}
	return DatabaseIdentity(database), nil
}

func validNativeCreationName(name string) bool {
	if len(name) == 0 || len(name) > 63 || name[0] < 'a' || name[0] > 'z' || name[len(name)-1] == '-' {
		return false
	}
	for _, char := range name {
		if !(char >= 'a' && char <= 'z' || char >= '0' && char <= '9' || char == '-') {
			return false
		}
	}
	return true
}
