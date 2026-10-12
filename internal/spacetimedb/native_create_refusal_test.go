package spacetimedb

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestNativeCreateRequiresExactCreatedResponse(t *testing.T) {
	for _, outcome := range []string{"disconnect", "redirect", "forbidden", "oversized", "updated", "missing-domain", "foreign-domain", "invalid-identity", "prefixed-identity", "metadata-identity", "extra-field", "duplicate-field", "trailing", "null", "denied"} {
		t.Run(outcome, func(t *testing.T) { nativeCreateUnknown(t, outcome) })
	}
}

func nativeCreateUnknown(t *testing.T, outcome string) {
	t.Helper()
	var puts, foreignRequests atomic.Int32
	foreign := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		foreignRequests.Add(1)
		_, _ = io.WriteString(w, nativeCreateResponse())
	}))
	defer foreign.Close()
	credential, wasm := nativeCreateCredential(t), nativePublicationWasm(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if request.Method == http.MethodGet {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		puts.Add(1)
		_, _ = io.Copy(io.Discard, request.Body)
		writeNativeCreateFailure(w, outcome, foreign.URL, credential.bearer)
	}))
	defer server.Close()
	endpoint := publisherVerificationEndpoint(t, server.URL)
	proof := nativeCreateVerified(t, endpoint, credential)
	receipt, err := PublishNativeCreate(context.Background(), endpoint, credential, proof, nativeCreationName, wasm)
	if !errors.Is(err, ErrNativePublicationUnknown) || receipt.BindingSHA256() != "" || strings.Contains(err.Error(), credential.bearer) || foreignRequests.Load() != 0 {
		t.Fatal("uncertain response created success, leaked a token or followed a redirect", err)
	}
	if _, err = PublishNativeCreate(context.Background(), endpoint, credential, proof, nativeCreationName, wasm); !errors.Is(err, ErrNativePublicationUnknown) || puts.Load() != 1 {
		t.Fatal("uncertain initial publication caused a retry", err)
	}
}

func writeNativeCreateFailure(w http.ResponseWriter, outcome, foreign, secret string) {
	switch outcome {
	case "disconnect":
		panic(http.ErrAbortHandler)
	case "redirect":
		w.Header().Set("Location", foreign+"/private?token="+secret)
		w.WriteHeader(http.StatusTemporaryRedirect)
	case "forbidden":
		w.WriteHeader(http.StatusForbidden)
		_, _ = io.WriteString(w, secret)
	case "oversized":
		_, _ = io.WriteString(w, strings.Repeat("x", NativePublicationResponseLimit+1))
	case "updated":
		_, _ = io.WriteString(w, strings.ReplaceAll(nativeCreateResponse(), "created", "updated"))
	case "missing-domain":
		_, _ = io.WriteString(w, strings.ReplaceAll(nativeCreateResponse(), `"`+nativeCreationName+`"`, "null"))
	case "foreign-domain":
		_, _ = io.WriteString(w, strings.ReplaceAll(nativeCreateResponse(), nativeCreationName, "foreign"))
	case "invalid-identity":
		_, _ = io.WriteString(w, strings.ReplaceAll(nativeCreateResponse(), databaseHex, "not-an-identity"))
	case "prefixed-identity":
		_, _ = io.WriteString(w, strings.ReplaceAll(nativeCreateResponse(), databaseHex, "0x"+databaseHex))
	case "metadata-identity":
		_, _ = io.WriteString(w, strings.ReplaceAll(nativeCreateResponse(), `"`+databaseHex+`"`, `{"__identity__":"0x`+databaseHex+`"}`))
	case "extra-field":
		_, _ = io.WriteString(w, strings.ReplaceAll(nativeCreateResponse(), `"op":`, `"extra":true,"op":`))
	case "duplicate-field":
		_, _ = io.WriteString(w, strings.ReplaceAll(nativeCreateResponse(), `"op":`, `"op":"created","op":`))
	case "trailing":
		_, _ = io.WriteString(w, nativeCreateResponse()+"{}")
	case "null":
		_, _ = io.WriteString(w, "null")
	case "denied":
		_, _ = io.WriteString(w, `{"PermissionDenied":{"name":"`+nativeCreationName+`"}}`)
	}
}

func TestNativeCreateIgnoresProxyAndBindsEndpointAndArtifact(t *testing.T) {
	var proxyRequests atomic.Int32
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		proxyRequests.Add(1)
		w.WriteHeader(http.StatusForbidden)
	}))
	defer proxy.Close()
	t.Setenv("HTTP_PROXY", proxy.URL)
	t.Setenv("http_proxy", proxy.URL)
	t.Setenv("ALL_PROXY", proxy.URL)
	t.Setenv("NO_PROXY", "")
	credential, wasm := nativeCreateCredential(t), nativePublicationWasm(t)
	handler := http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if request.Method == http.MethodGet {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		_, _ = io.Copy(io.Discard, request.Body)
		_, _ = io.WriteString(w, nativeCreateResponse())
	})
	firstServer, secondServer := httptest.NewServer(handler), httptest.NewServer(handler)
	defer firstServer.Close()
	defer secondServer.Close()
	firstEndpoint, secondEndpoint := publisherVerificationEndpoint(t, firstServer.URL), publisherVerificationEndpoint(t, secondServer.URL)
	proof := nativeCreateVerified(t, firstEndpoint, credential)
	if _, err := PublishNativeCreate(context.Background(), secondEndpoint, credential, proof, nativeCreationName, wasm); !errors.Is(err, ErrNativePublicationRefused) {
		t.Fatal("creation consumed authentication from another endpoint", err)
	}
	first := nativeCreateBoundReceipt(t, firstEndpoint, credential, wasm)
	second := nativeCreateBoundReceipt(t, secondEndpoint, credential, wasm)
	if first.BindingSHA256() == second.BindingSHA256() || first.At(secondEndpoint) || first.SHA256() != second.SHA256() {
		t.Fatal("identical success bytes lost endpoint provenance")
	}
	wasm[len(wasm)-1] ^= 1
	changed := nativeCreateBoundReceipt(t, firstEndpoint, credential, wasm)
	if first.WasmSHA256() == changed.WasmSHA256() || first.ProgramWireHash() == changed.ProgramWireHash() || first.BindingSHA256() == changed.BindingSHA256() || first.SHA256() != changed.SHA256() || proxyRequests.Load() != 0 {
		t.Fatal("identical success bytes lost candidate provenance or used proxy")
	}
}

func nativeCreateBoundReceipt(t *testing.T, endpoint PrivateServerEndpoint, credential PublisherCredential, wasm []byte) NativeCreateReceipt {
	t.Helper()
	proof := nativeCreateVerified(t, endpoint, credential)
	receipt, err := PublishNativeCreate(context.Background(), endpoint, credential, proof, nativeCreationName, wasm)
	if err != nil {
		t.Fatal(err)
	}
	return receipt
}
