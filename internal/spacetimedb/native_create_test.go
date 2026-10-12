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
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

const nativeCreationName = "owned-fixture"

func nativeCreateResponse() string {
	return `{"Success":{"domain":"` + nativeCreationName + `","database_identity":"` + databaseHex + `","op":"created"}}`
}

func nativeCreateVerified(t *testing.T, endpoint PrivateServerEndpoint, credential PublisherCredential) NativePublisherVerification {
	t.Helper()
	proof, err := VerifyNativePublisher(context.Background(), endpoint, credential)
	if err != nil {
		t.Fatal(err)
	}
	return proof
}

func nativeCreateCredential(t *testing.T) PublisherCredential {
	t.Helper()
	return publisherVerificationCredential(t, `{"hex_identity":"`+publisherHex+`"}`)
}

func TestNativeCreateRetainsActualFixedRequestAndResponse(t *testing.T) {
	wasm, credential := nativePublicationWasm(t), nativeCreateCredential(t)
	var puts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if request.Method == http.MethodGet {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		puts.Add(1)
		assertNativeCreateRequest(t, request, credential, wasm)
		_, _ = io.WriteString(w, nativeCreateResponse())
	}))
	defer server.Close()
	endpoint := publisherVerificationEndpoint(t, server.URL)
	proof := nativeCreateVerified(t, endpoint, credential)
	copyOfProof := proof
	receipt, err := PublishNativeCreate(context.Background(), endpoint, credential, proof, nativeCreationName, wasm)
	sha, program, _ := ModuleIdentities(wasm)
	if err != nil || receipt.Database() != DatabaseIdentity(databaseHex) || receipt.Publisher() != proof.Publisher() || receipt.Name() != nativeCreationName || receipt.WasmSHA256() != sha || receipt.ProgramWireHash() != program || receipt.PublisherCredentialSHA256() != proof.CredentialSHA256() || receipt.VerificationSHA256() != proof.SHA256() || receipt.SHA256() != publicationBytesSHA256([]byte(nativeCreateResponse())) || receipt.BindingSHA256() == "" || !receipt.At(endpoint) {
		t.Fatal("initial native receipt lost actual request or response binding", err)
	}
	receipt.ResponseBytes()[0] = 'x'
	proof.StorageBytes()[0] = 'x'
	if receipt.SHA256() != publicationBytesSHA256(receipt.ResponseBytes()) || receipt.VerificationSHA256() != proof.SHA256() {
		t.Fatal("returned bytes mutated owned native create evidence")
	}
	if _, err = PublishNativeCreate(context.Background(), endpoint, credential, copyOfProof, nativeCreationName, wasm); !errors.Is(err, ErrNativePublicationUnknown) || puts.Load() != 1 {
		t.Fatal("copied verification replayed initial publication", err)
	}
	if _, err = json.Marshal(receipt); err == nil || strings.Contains(fmt.Sprintf("%s %#v", receipt, receipt), credential.bearer) {
		t.Fatal("private initial publication acquired a public representation")
	}
	if (NativeCreateReceipt{}).BindingSHA256() != "" || (NativeCreateReceipt{}).At(endpoint) || (NativeCreateReceipt{}).SHA256() != "" {
		t.Fatal("zero receipt manufactured initial publication provenance")
	}
}

func assertNativeCreateRequest(t *testing.T, request *http.Request, credential PublisherCredential, wasm []byte) {
	t.Helper()
	raw, err := io.ReadAll(request.Body)
	if err != nil || !bytes.Equal(raw, wasm) || request.Header.Get("Authorization") != "Bearer "+credential.bearer || request.Header.Get("Content-Type") != "application/octet-stream" || request.Header.Get("Accept") != "application/json" || !request.Close {
		t.Error("initial native request body, header or single connection changed")
	}
	query := request.URL.Query()
	if request.Method != http.MethodPut || request.URL.Path != "/v1/database/"+nativeCreationName || len(query) != 3 || query.Get("host_type") != "Wasm" || query.Get("policy") != "Compatible" || query.Get("num_replicas") != "1" {
		t.Error("initial native request added allocation, clear, parent, token or other options")
	}
}

func TestNativeCreateRefusesUnboundInputsWithoutConsumingVerification(t *testing.T) {
	credential, wasm := nativeCreateCredential(t), nativePublicationWasm(t)
	var puts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if request.Method == http.MethodGet {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		puts.Add(1)
		_, _ = io.Copy(io.Discard, request.Body)
		_, _ = io.WriteString(w, nativeCreateResponse())
	}))
	defer server.Close()
	endpoint := publisherVerificationEndpoint(t, server.URL)
	proof := nativeCreateVerified(t, endpoint, credential)
	for _, name := range []string{"", "-bad", "bad-", "Bad", "1bad", "../bad", "org/bad", "bad?clear=true", strings.Repeat("a", 64), databaseHex} {
		_, err := PublishNativeCreate(context.Background(), endpoint, credential, proof, name, wasm)
		if !errors.Is(err, ErrNativePublicationRefused) || puts.Load() != 0 {
			t.Fatal("invalid namespace caused a native request", err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	for _, input := range []struct {
		ctx        context.Context
		endpoint   PrivateServerEndpoint
		credential PublisherCredential
		proof      NativePublisherVerification
		wasm       []byte
	}{
		{ctx, endpoint, credential, proof, wasm},
		{context.Background(), PrivateServerEndpoint{}, credential, proof, wasm},
		{context.Background(), endpoint, PublisherCredential{}, proof, wasm},
		{context.Background(), endpoint, publisherVerificationCredential(t, `{"hex_identity":"`+publisherHex+`","different":true}`), proof, wasm},
		{context.Background(), endpoint, credential, NativePublisherVerification{}, wasm},
		{context.Background(), endpoint, credential, proof, []byte("invalid")},
		{context.Background(), endpoint, credential, proof, nil},
		{context.Background(), endpoint, credential, proof, make([]byte, MaxWasmBytes+1)},
	} {
		_, err := PublishNativeCreate(input.ctx, input.endpoint, input.credential, input.proof, nativeCreationName, input.wasm)
		if !errors.Is(err, ErrNativePublicationRefused) || puts.Load() != 0 {
			t.Fatal("unbound creation input caused a native request", err)
		}
	}
	if _, err := PublishNativeCreate(context.Background(), endpoint, credential, proof, nativeCreationName, wasm); err != nil || puts.Load() != 1 {
		t.Fatal("pre-request refusal consumed a valid verification", err)
	}
}

func TestNativeCreateCopiesShareOneAttemptUnderRace(t *testing.T) {
	var puts, successful, unknown atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if request.Method == http.MethodGet {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		puts.Add(1)
		_, _ = io.Copy(io.Discard, request.Body)
		_, _ = io.WriteString(w, nativeCreateResponse())
	}))
	defer server.Close()
	endpoint, credential, wasm := publisherVerificationEndpoint(t, server.URL), nativeCreateCredential(t), nativePublicationWasm(t)
	proof := nativeCreateVerified(t, endpoint, credential)
	var group sync.WaitGroup
	for range 16 {
		group.Go(func() {
			_, err := PublishNativeCreate(context.Background(), endpoint, credential, proof, nativeCreationName, wasm)
			if err == nil {
				successful.Add(1)
				return
			}
			if errors.Is(err, ErrNativePublicationUnknown) {
				unknown.Add(1)
				return
			}
			t.Error(err)
		})
	}
	group.Wait()
	if successful.Load() != 1 || unknown.Load() != 15 || puts.Load() != 1 {
		t.Fatal("racing copied proofs sent multiple initial publications")
	}
}

func TestNativeCreateTimeoutAfterRequestIsUnknownAndCannotRetry(t *testing.T) {
	var puts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if request.Method == http.MethodGet {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		puts.Add(1)
		_, _ = io.Copy(io.Discard, request.Body)
		<-request.Context().Done()
	}))
	defer server.Close()
	endpoint, credential, wasm := publisherVerificationEndpoint(t, server.URL), nativeCreateCredential(t), nativePublicationWasm(t)
	proof := nativeCreateVerified(t, endpoint, credential)
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if _, err := PublishNativeCreate(ctx, endpoint, credential, proof, nativeCreationName, wasm); !errors.Is(err, ErrNativePublicationUnknown) {
		t.Fatal("timed out creation appeared known", err)
	}
	if _, err := PublishNativeCreate(context.Background(), endpoint, credential, proof, nativeCreationName, wasm); !errors.Is(err, ErrNativePublicationUnknown) || puts.Load() != 1 {
		t.Fatal("unknown creation was retried", err)
	}
}
