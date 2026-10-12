package spacetimedb

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func publisherVerificationCredential(t *testing.T, claims string) PublisherCredential {
	t.Helper()
	token := "e30." + base64.RawURLEncoding.EncodeToString([]byte(claims)) + ".test-signature"
	credential, err := NewPublisherCredential([]byte(token))
	if err != nil {
		t.Fatal(err)
	}
	return credential
}

func publisherVerificationEndpoint(t *testing.T, address string) PrivateServerEndpoint {
	t.Helper()
	parsed, err := url.Parse(address)
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.ParseUint(parsed.Port(), 10, 16)
	if err != nil {
		t.Fatal(err)
	}
	endpoint, err := NewPrivateServerEndpoint(parsed.Hostname(), uint16(port))
	if err != nil {
		t.Fatal(err)
	}
	return endpoint
}

func TestNativePublisherVerificationRequiresTheFixedAuthenticatedResponse(t *testing.T) {
	credential := publisherVerificationCredential(t, `{"hex_identity":"`+publisherHex+`","iss":"supplied-key","sub":"existing-owner"}`)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodGet || request.URL.Path != "/v1/identity/"+publisherHex+"/verify" || request.URL.RawQuery != "" || request.Header.Get("Authorization") != "Bearer "+credential.bearer {
			t.Error("verification changed purpose or allocated an owner")
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	endpoint := publisherVerificationEndpoint(t, server.URL)
	before := time.Now().UTC()
	proof, err := VerifyNativePublisher(context.Background(), endpoint, credential)
	if err != nil || proof.Publisher() != PublisherIdentity(publisherHex) || proof.CredentialSHA256() != publicationBytesSHA256([]byte(credential.bearer)) || proof.VerifiedAt().Before(before) || proof.VerifiedAt().Location() != time.UTC || proof.SHA256() == "" || !proof.At(endpoint) {
		t.Fatal("actual verification response lost its identity/credential binding", err)
	}
	encoded := proof.StorageBytes()
	var transcript struct {
		Endpoint string `json:"endpoint_sha256"`
		Method   string `json:"method"`
		Path     string `json:"path"`
		Status   int    `json:"status"`
	}
	if err := json.Unmarshal(encoded, &transcript); err != nil || transcript.Endpoint != publicationBytesSHA256([]byte(endpoint.base)) || transcript.Method != http.MethodGet || transcript.Path != "/v1/identity/"+publisherHex+"/verify" || transcript.Status != http.StatusNoContent {
		t.Fatal("retained verification lost the actual request/result transcript", err)
	}
	encoded[0] = 'x'
	if proof.SHA256() != publicationBytesSHA256(proof.StorageBytes()) {
		t.Fatal("returned storage bytes mutated verification evidence")
	}
	if _, err = json.Marshal(proof); err == nil || strings.Contains(fmt.Sprintf("%s %#v", proof, proof), credential.bearer) {
		t.Fatal("publisher credential became public")
	}
	if (NativePublisherVerification{}).At(endpoint) || (NativePublisherVerification{}).SHA256() != "" {
		t.Fatal("zero verification manufactured native authentication")
	}
}

func TestNativePublisherVerificationNeverAcceptsAnotherResponseOrRedirect(t *testing.T) {
	var foreignRequests atomic.Int32
	foreign := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		foreignRequests.Add(1)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer foreign.Close()
	credential := publisherVerificationCredential(t, `{"hex_identity":"`+publisherHex+`"}`)
	for _, status := range []int{http.StatusOK, http.StatusBadRequest, http.StatusUnauthorized, http.StatusInternalServerError, http.StatusTemporaryRedirect} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Location", foreign.URL+"/private?token="+credential.bearer)
			w.WriteHeader(status)
			_, _ = fmt.Fprint(w, credential.bearer)
		}))
		proof, err := VerifyNativePublisher(context.Background(), publisherVerificationEndpoint(t, server.URL), credential)
		server.Close()
		if !errors.Is(err, ErrNativePublisherVerification) || proof.SHA256() != "" || strings.Contains(err.Error(), credential.bearer) || foreignRequests.Load() != 0 {
			t.Fatal("unverified response, redirect or private failure minted publisher proof", err)
		}
	}
}

func TestNativePublisherVerificationRefusesMissingOrForgedIdentitySyntaxBeforeRequest(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	endpoint := publisherVerificationEndpoint(t, server.URL)
	for _, claims := range []string{`{}`, `{"hex_identity":null}`, `{"hex_identity":"0x` + publisherHex + `"}`, `{"hex_identity":{"__identity__":"0x` + publisherHex + `"}}`, `{"hex_identity":"` + publisherHex + `","hex_identity":"` + publisherHex + `"}`, `{"hex_identity":"` + strings.ToUpper(publisherHex) + `"}`} {
		credential := publisherVerificationCredential(t, claims)
		if _, err := VerifyNativePublisher(context.Background(), endpoint, credential); !errors.Is(err, ErrNativePublisherVerification) || requests.Load() != 0 {
			t.Fatal("unqualified identity syntax caused native authentication or allocation", err)
		}
	}
}

func TestNativePublisherServerEndpointAllowsOnlyLiteralLoopback(t *testing.T) {
	for _, host := range []string{"localhost", "example.test", "192.0.2.1", "127.0.0.1/private"} {
		if _, err := NewPrivateServerEndpoint(host, 23991); !errors.Is(err, ErrObservation) {
			t.Fatal("nonliteral private server location accepted", err)
		}
	}
	for _, host := range []string{"127.0.0.1", "::1"} {
		if _, err := NewPrivateServerEndpoint(host, 23991); err != nil {
			t.Fatal("literal loopback private server refused", err)
		}
	}
}
