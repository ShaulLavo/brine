package spacetimedb

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"net/url"
	"time"
)

const nativePublicationWait = 15 * time.Second

func publicationBytesSHA256(raw []byte) string {
	hash := sha256.Sum256(raw)
	return hex.EncodeToString(hash[:])
}

func nativeModuleRequest(ctx context.Context, endpoint PrivateServerEndpoint, credential PublisherCredential, method, path string, query url.Values, wasm []byte) ([]byte, error) {
	wait, cancel := context.WithTimeout(ctx, nativePublicationWait)
	defer cancel()
	transport := &http.Transport{Proxy: nil, DisableKeepAlives: true, MaxResponseHeaderBytes: 64 << 10, ResponseHeaderTimeout: nativePublicationWait}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return ErrNativePreflight }}
	request, err := http.NewRequestWithContext(wait, method, endpoint.base+path+"?"+query.Encode(), bytes.NewReader(wasm))
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
