package spacetimedb

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"slices"
	"strings"
	"time"
)

var ErrNativePublisherVerification = errors.New("private native publisher verification refused")

type NativePublisherVerification struct {
	endpoint      PrivateServerEndpoint
	publisher     PublisherIdentity
	credentialSHA string
	verifiedAt    time.Time
	encoded       []byte
}

func (v NativePublisherVerification) Publisher() PublisherIdentity { return v.publisher }
func (v NativePublisherVerification) CredentialSHA256() string     { return v.credentialSHA }
func (v NativePublisherVerification) VerifiedAt() time.Time        { return v.verifiedAt }
func (v NativePublisherVerification) SHA256() string {
	if len(v.encoded) == 0 {
		return ""
	}
	return publicationBytesSHA256(v.encoded)
}
func (v NativePublisherVerification) StorageBytes() []byte { return slices.Clone(v.encoded) }
func (v NativePublisherVerification) At(endpoint PrivateServerEndpoint) bool {
	return v.endpoint.base != "" && v.endpoint == endpoint && len(v.encoded) > 0
}
func (NativePublisherVerification) String() string { return "[private native publisher verification]" }
func (NativePublisherVerification) GoString() string {
	return "[private native publisher verification]"
}
func (NativePublisherVerification) MarshalJSON() ([]byte, error) { return nil, ErrPrivateCredential }

// VerifyNativePublisher authenticates an existing supplied-key JWT identity.
// The signed hex_identity is an expectation until the owned private runtime
// confirms it. This request never allocates an identity or mints a new token.
func VerifyNativePublisher(ctx context.Context, endpoint PrivateServerEndpoint, credential PublisherCredential) (NativePublisherVerification, error) {
	publisher, err := claimedNativePublisher(credential)
	if err != nil || endpoint.base == "" || ctx.Err() != nil {
		return NativePublisherVerification{}, ErrNativePublisherVerification
	}
	wait, cancel := context.WithTimeout(ctx, nativePublicationWait)
	defer cancel()
	transport := &http.Transport{Proxy: nil, DisableKeepAlives: true, MaxResponseHeaderBytes: 64 << 10, ResponseHeaderTimeout: nativePublicationWait}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return ErrNativePublisherVerification }}
	request, err := http.NewRequestWithContext(wait, http.MethodGet, endpoint.base+"/v1/identity/"+string(publisher)+"/verify", nil)
	if err != nil {
		return NativePublisherVerification{}, ErrNativePublisherVerification
	}
	request.Close = true
	request.Header.Set("Authorization", "Bearer "+credential.bearer)
	response, err := client.Do(request)
	if err != nil {
		return NativePublisherVerification{}, ErrNativePublisherVerification
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusNoContent {
		return NativePublisherVerification{}, ErrNativePublisherVerification
	}
	raw, err := io.ReadAll(io.LimitReader(response.Body, 1))
	if err != nil || len(raw) != 0 || wait.Err() != nil {
		return NativePublisherVerification{}, ErrNativePublisherVerification
	}
	verified := NativePublisherVerification{endpoint: endpoint, publisher: publisher, credentialSHA: publicationBytesSHA256([]byte(credential.bearer)), verifiedAt: time.Now().UTC()}
	verified.encoded, err = json.Marshal(struct {
		Purpose       string            `json:"purpose"`
		EndpointSHA   string            `json:"endpoint_sha256"`
		Method        string            `json:"method"`
		Path          string            `json:"path"`
		Status        int               `json:"status"`
		Publisher     PublisherIdentity `json:"publisher"`
		CredentialSHA string            `json:"credential_sha256"`
		VerifiedAt    time.Time         `json:"verified_at"`
	}{"native_supplied_key_publisher_verification", endpoint.SHA256(), http.MethodGet, request.URL.EscapedPath(), http.StatusNoContent, publisher, verified.credentialSHA, verified.verifiedAt})
	if err != nil {
		return NativePublisherVerification{}, ErrNativePublisherVerification
	}
	return verified, nil
}

func claimedNativePublisher(credential PublisherCredential) (PublisherIdentity, error) {
	if !validJWTText(credential.bearer) {
		return "", ErrNativePublisherVerification
	}
	parts := strings.Split(credential.bearer, ".")
	raw, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil || len(raw) > 16<<10 {
		return "", ErrNativePublisherVerification
	}
	defer clear(raw)
	limits := DefaultSchemaLimits()
	limits.MaxBytes = 16 << 10
	value, err := decodeDocument(raw, limits)
	if err != nil {
		return "", ErrNativePublisherVerification
	}
	claims, valid := value.(map[string]any)
	if !valid {
		return "", ErrNativePublisherVerification
	}
	identity, valid := claims["hex_identity"].(string)
	if !valid || !validHexIdentity(identity) {
		return "", ErrNativePublisherVerification
	}
	return PublisherIdentity(identity), nil
}
