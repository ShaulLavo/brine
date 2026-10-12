package spacetimedb

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
)

const nativeTestMigrationToken = "0xdddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd"

func nativePublicationWasm(t *testing.T) []byte {
	t.Helper()
	raw, err := os.ReadFile("../spec/testdata/managed-module.wasm")
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func nativePublicationEndpoint(t *testing.T, address string) PrivateEndpoint {
	t.Helper()
	parsed, err := url.Parse(address)
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.ParseUint(parsed.Port(), 10, 16)
	if err != nil {
		t.Fatal(err)
	}
	endpoint, err := NewPrivateEndpoint(parsed.Hostname(), uint16(port), databaseHex)
	if err != nil {
		t.Fatal(err)
	}
	return endpoint
}

func nativePublicationCredential(t *testing.T) PublisherCredential {
	t.Helper()
	credential, err := NewPublisherCredential([]byte(privateTestToken))
	if err != nil {
		t.Fatal(err)
	}
	return credential
}

func automaticNativePreflight() string {
	return `{"AutoMigrate":{"migrate_plan":"fixed native plan","break_clients":false,"token":"` + nativeTestMigrationToken + `","major_version_upgrade":false}}`
}

func nativeUpdateReceipt() string {
	return `{"Success":{"domain":null,"database_identity":{"__identity__":"0x` + databaseHex + `"},"op":"updated"}}`
}

func TestNativePublicationUsesOneFixedPrivateTokenBoundUpdate(t *testing.T) {
	wasm := nativePublicationWasm(t)
	var puts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		assertNativePublicationRequest(t, request, wasm)
		if request.Method == http.MethodPost {
			_, _ = io.WriteString(w, automaticNativePreflight())
			return
		}
		puts.Add(1)
		_, _ = io.WriteString(w, nativeUpdateReceipt())
	}))
	defer server.Close()
	endpoint, credential := nativePublicationEndpoint(t, server.URL), nativePublicationCredential(t)
	preflight, err := PreflightNativePublication(context.Background(), endpoint, credential, wasm)
	if err != nil || !preflight.Automatic() || preflight.BreaksClients() || preflight.MajorUpgrade() || preflight.ResponseSHA256() != publicationBytesSHA256([]byte(automaticNativePreflight())) || preflight.TokenSHA256() != publicationBytesSHA256([]byte(nativeTestMigrationToken)) {
		t.Fatal("actual private preflight lost exact bytes or typed fields", err)
	}
	preflight.ResponseBytes()[0] = 'x'
	preflight.TokenBytes()[0] = 'x'
	copyOfPreflight := *preflight
	receipt, err := PublishNativeUpdate(context.Background(), endpoint, credential, wasm, preflight)
	if err != nil || receipt.Database() != DatabaseIdentity(databaseHex) || receipt.SHA256() != publicationBytesSHA256([]byte(nativeUpdateReceipt())) {
		t.Fatal("fixed native update receipt did not match", err)
	}
	if _, err = PublishNativeUpdate(context.Background(), endpoint, credential, wasm, &copyOfPreflight); !errors.Is(err, ErrNativePublicationUnknown) || puts.Load() != 1 {
		t.Fatal("copied native preflight replayed an already consumed update", err)
	}
	if _, err = json.Marshal(preflight); err == nil {
		t.Fatal("private native token acquired a public JSON representation")
	}
	if _, err = json.Marshal(receipt); err == nil {
		t.Fatal("private native receipt acquired a public JSON representation")
	}
	if strings.Contains(fmt.Sprintf("%s %#v %#v", preflight, preflight, copyOfPreflight), nativeTestMigrationToken) {
		t.Fatal("native token entered formatting")
	}
}

func assertNativePublicationRequest(t *testing.T, request *http.Request, expected []byte) {
	t.Helper()
	raw, err := io.ReadAll(request.Body)
	if err != nil || !bytes.Equal(raw, expected) || request.Header.Get("Authorization") != "Bearer "+privateTestToken || request.Header.Get("Content-Type") != "application/octet-stream" || request.Header.Get("Accept") != "application/json" {
		t.Error("native private request bytes or headers changed")
	}
	query := request.URL.Query()
	if request.Method == http.MethodPost {
		if request.URL.Path != "/v1/database/"+databaseHex+"/pre_publish" || len(query) != 2 || query.Get("host_type") != "Wasm" || query.Get("style") != "NoColor" {
			t.Error("native preflight purpose changed")
		}
		return
	}
	if request.Method != http.MethodPut || request.URL.Path != "/v1/database/"+databaseHex || len(query) != 4 || query.Get("host_type") != "Wasm" || query.Get("policy") != "BreakClients" || query.Get("token") != nativeTestMigrationToken || query.Get("update_confirmation_timeout") != "5s" {
		t.Error("native update lost its exact token or added unreviewed flags")
	}
}

func TestNativePublicationUnknownEffectCannotRetryOrExposeResponse(t *testing.T) {
	for _, outcome := range []string{"disconnect", "redirect", "forbidden", "oversized", "wrong-database", "created", "extra-field", "duplicate-field"} {
		t.Run(outcome, func(t *testing.T) { nativePublicationUnknown(t, outcome) })
	}
}

func nativePublicationUnknown(t *testing.T, outcome string) {
	t.Helper()
	var puts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		_, _ = io.Copy(io.Discard, request.Body)
		if request.Method == http.MethodPost {
			_, _ = io.WriteString(w, automaticNativePreflight())
			return
		}
		puts.Add(1)
		writeNativePublicationUnknown(w, outcome)
	}))
	defer server.Close()
	endpoint, credential, wasm := nativePublicationEndpoint(t, server.URL), nativePublicationCredential(t), nativePublicationWasm(t)
	preflight, err := PreflightNativePublication(context.Background(), endpoint, credential, wasm)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = PublishNativeUpdate(context.Background(), endpoint, credential, wasm, preflight); !errors.Is(err, ErrNativePublicationUnknown) || strings.Contains(err.Error(), privateTestToken) || strings.Contains(err.Error(), nativeTestMigrationToken) {
		t.Fatal("uncertain native effect lost inspection or exposed private error content", err)
	}
	if _, err = PublishNativeUpdate(context.Background(), endpoint, credential, wasm, preflight); !errors.Is(err, ErrNativePublicationUnknown) || puts.Load() != 1 {
		t.Fatal("uncertain native effect repeated its HTTP mutation", err)
	}
}

func writeNativePublicationUnknown(w http.ResponseWriter, outcome string) {
	switch outcome {
	case "disconnect":
		connection, _, err := w.(http.Hijacker).Hijack()
		if err == nil {
			_ = connection.Close()
		}
	case "redirect":
		w.Header().Set("Location", "http://127.0.0.1:1/private?token="+nativeTestMigrationToken)
		w.WriteHeader(http.StatusTemporaryRedirect)
	case "forbidden":
		http.Error(w, privateTestToken+nativeTestMigrationToken, http.StatusForbidden)
	case "oversized":
		_, _ = io.WriteString(w, strings.Repeat("x", NativePublicationResponseLimit+1))
	case "wrong-database":
		_, _ = io.WriteString(w, strings.ReplaceAll(nativeUpdateReceipt(), databaseHex, publisherHex))
	case "created":
		_, _ = io.WriteString(w, strings.ReplaceAll(nativeUpdateReceipt(), "updated", "created"))
	case "extra-field":
		_, _ = io.WriteString(w, strings.Replace(nativeUpdateReceipt(), `"domain":null`, `"extra":true,"domain":null`, 1))
	case "duplicate-field":
		_, _ = io.WriteString(w, strings.Replace(nativeUpdateReceipt(), `"op":"updated"`, `"op":"updated","op":"updated"`, 1))
	}
}

func TestNativePublicationPreflightRefusesUnsupportedPrivateResponses(t *testing.T) {
	for _, response := range []string{
		`{"AutoMigrate":{"migrate_plan":"x","break_clients":false,"token":"wrong","major_version_upgrade":false}}`,
		strings.Replace(automaticNativePreflight(), `"token":`, `"extra":true,"token":`, 1),
		strings.Replace(automaticNativePreflight(), `"break_clients":false`, `"break_clients":null`, 1),
		strings.Replace(automaticNativePreflight(), `"major_version_upgrade":false`, `"major_version_upgrade":false,"major_version_upgrade":false`, 1),
		`{"FutureMigrate":{}}`,
		`{"ManualMigrate":{"reason":"operator procedure","major_version_upgrade":false,"token":"private"}}`,
		strings.Repeat("x", NativePublicationResponseLimit+1),
	} {
		if _, err := decodeNativePublicationPreflight([]byte(response)); !errors.Is(err, ErrNativePreflight) {
			t.Fatal("unknown native preflight syntax accepted", err)
		}
	}
}

func TestNativePublicationRefusesManualMajorAndMismatchedScopeBeforeEffect(t *testing.T) {
	for _, attack := range []string{"manual", "major", "artifact", "database", "publisher", "empty-preview"} {
		t.Run(attack, func(t *testing.T) { nativePublicationBeforeEffect(t, attack) })
	}
}

func nativePublicationBeforeEffect(t *testing.T, attack string) {
	t.Helper()
	var puts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		_, _ = io.Copy(io.Discard, request.Body)
		if request.Method != http.MethodPost {
			puts.Add(1)
			return
		}
		response := automaticNativePreflight()
		if attack == "manual" {
			response = `{"ManualMigrate":{"reason":"operator procedure","major_version_upgrade":false}}`
		}
		if attack == "major" {
			response = strings.Replace(response, `"major_version_upgrade":false`, `"major_version_upgrade":true`, 1)
		}
		_, _ = io.WriteString(w, response)
	}))
	defer server.Close()
	endpoint, credential, wasm := nativePublicationEndpoint(t, server.URL), nativePublicationCredential(t), nativePublicationWasm(t)
	preflight, err := PreflightNativePublication(context.Background(), endpoint, credential, wasm)
	if err != nil {
		t.Fatal(err)
	}
	switch attack {
	case "artifact":
		wasm[len(wasm)-1] ^= 1
	case "database":
		endpoint.database = DatabaseIdentity(publisherHex)
	case "publisher":
		credential.bearer = "different-header.different-payload.different-signature"
	case "empty-preview":
		preflight = &NativePublicationPreflight{}
	}
	if _, err = PublishNativeUpdate(context.Background(), endpoint, credential, wasm, preflight); !errors.Is(err, ErrNativePublicationRefused) || puts.Load() != 0 {
		t.Fatal("unsupported or unbound preflight issued native publication", err)
	}
}
