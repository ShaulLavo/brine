package restore

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func probeInputs(endpoint string) (Destination, Credentials) {
	return Destination{Endpoint: endpoint, Region: "auto", Bucket: "backup-bucket", Prefix: "epoch", PathStyle: true}, Credentials{AccessKey: "PLANTED_ACCESS", SecretKey: "PLANTED_SECRET", SessionToken: "PLANTED_SESSION"}
}

func probeListing(key string) string {
	contents, count := "", 0
	if key != "" {
		contents, count = "<Contents><Key>"+key+"</Key></Contents>", 1
	}
	return fmt.Sprintf(`<ListBucketResult><Name>backup-bucket</Name><Prefix>epoch/</Prefix><MaxKeys>1</MaxKeys><KeyCount>%d</KeyCount>%s</ListBucketResult>`, count, contents)
}

func TestProbeRemoteClassifiesBoundedResponses(t *testing.T) {
	tests := []struct {
		name   string
		status int
		body   string
		want   RemoteState
	}{
		{"reachable", 200, probeListing("epoch/ltx/one"), RemoteReachable},
		{"empty", 200, probeListing(""), RemoteEmpty},
		{"expired", 403, `<Error><Code>ExpiredToken</Code><Message>PLANTED_SECRET</Message></Error>`, RemoteCredentialsRejected},
		{"invalid_key", 401, `<Error><Code>InvalidAccessKeyId</Code></Error>`, RemoteCredentialsRejected},
		{"signature", 403, `<Error><Code>SignatureDoesNotMatch</Code></Error>`, RemoteCredentialsRejected},
		{"denied", 403, `<Error><Code>AccessDenied</Code></Error>`, RemoteAuthRefused},
		{"other_denied", 403, `PLANTED_SECRET`, RemoteAuthRefused},
		{"missing_bucket", 404, `<Error><Code>NoSuchBucket</Code></Error>`, RemoteDestinationMissing},
		{"other_missing", 404, `<Error><Code>NoSuchKey</Code></Error>`, RemoteResponseInvalid},
		{"outage", 503, `<Error><Code>SlowDown</Code></Error>`, RemoteStorageUnavailable},
		{"rate_limited", 429, "PLANTED_SECRET", RemoteStorageUnavailable},
		{"malformed", 200, "PLANTED_SECRET", RemoteResponseInvalid},
		{"no_body", 200, "", RemoteResponseInvalid},
		{"wrong_scope", 200, probeListing("epoch-foreign/one"), RemoteResponseInvalid},
		{"trailing", 200, probeListing("") + "<Other/>", RemoteResponseInvalid},
		{"oversized_listing", 200, strings.Repeat("x", (64<<10)+1), RemoteResponseInvalid},
		{"oversized_error", 403, `<Error><Code>ExpiredToken</Code><Message>` + strings.Repeat("x", 64<<10) + `</Message></Error>`, RemoteResponseInvalid},
		{"trailing_error", 403, `<Error><Code>ExpiredToken</Code></Error><Other/>`, RemoteAuthRefused},
		{"redirect", 302, "", RemoteResponseInvalid},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) { checkProbeResponse(t, test.status, test.body, test.want) })
	}
}

func checkProbeResponse(t *testing.T, status int, body string, want RemoteState) {
	t.Helper()
	var calls atomic.Int64
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Method != http.MethodGet || r.URL.Path != "/backup-bucket" || r.URL.Query().Get("prefix") != "epoch/" || r.URL.Query().Get("max-keys") != "1" || r.Header.Get("Authorization") == "" || r.Header.Get("X-Amz-Security-Token") != "PLANTED_SESSION" {
			t.Error("probe was not a signed, scoped, read-only listing")
		}
		w.Header().Set("Location", "/redirect")
		w.WriteHeader(status)
		_, _ = fmt.Fprint(w, body)
	}))
	t.Cleanup(server.Close)
	server.Start()
	d, c := probeInputs(server.URL)
	before := time.Now()
	result := probeRemote(context.Background(), d, c, true)
	if result.State != want || result.ObservedAt.Before(before) || result.ObservedAt.After(time.Now()) || calls.Load() != 1 {
		t.Fatalf("observation=%+v want=%s calls=%d", result, want, calls.Load())
	}
	raw, err := json.Marshal(result)
	if err != nil || strings.Contains(string(raw), "PLANTED") || strings.Contains(fmt.Sprintf("%+v", result), "PLANTED") {
		t.Fatal("probe leaked provider or credential text", err)
	}
}

func TestProbeRemoteSkipsUnusableCredentialsAndUnsafeDestinations(t *testing.T) {
	var calls atomic.Int64
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { calls.Add(1); _, _ = fmt.Fprint(w, probeListing("")) }))
	t.Cleanup(server.Close)
	server.Start()
	d, c := probeInputs(server.URL)
	invalid := []Credentials{{}, {AccessKey: c.AccessKey}, {AccessKey: c.AccessKey, SecretKey: c.SecretKey, ExpiresAt: time.Now().Add(-time.Second)}, {AccessKey: c.AccessKey, SecretKey: c.SecretKey, ExpiresAt: time.Now().Add(time.Second)}}
	for _, credentials := range invalid {
		if got := probeRemote(context.Background(), d, credentials, true); got.State != RemoteNotChecked {
			t.Fatalf("unusable credential checked: %+v", got)
		}
	}
	if got := ProbeRemote(context.Background(), d, c); got.State != RemoteNotChecked || calls.Load() != 0 {
		t.Fatalf("production HTTP allowed: %+v", got)
	}
	d.Prefix = "../escape"
	if got := probeRemote(context.Background(), d, c, true); got.State != RemoteNotChecked || calls.Load() != 0 {
		t.Fatalf("unsafe destination checked: %+v", got)
	}
}

func TestProbeRemoteNetworkFailureAndCancellation(t *testing.T) {
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	t.Cleanup(server.Close)
	server.Start()
	server.Close()
	d, c := probeInputs(server.URL)
	if got := probeRemote(context.Background(), d, c, true); got.State != RemoteNetworkUnavailable {
		t.Fatalf("network error: %+v", got)
	}
	started := make(chan struct{})
	server = httptest.NewUnstartedServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) { close(started); <-r.Context().Done() }))
	t.Cleanup(server.Close)
	server.Start()
	d.Endpoint = server.URL
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	done := make(chan RemoteProbe, 1)
	go func() { done <- probeRemote(ctx, d, c, true) }()
	<-started
	cancel()
	select {
	case got := <-done:
		if got.State != RemoteNetworkUnavailable {
			t.Fatalf("cancellation: %+v", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("probe did not honor caller cancellation")
	}
}

func TestProbeRemoteInterruptedBodyIsNetworkUnavailable(t *testing.T) {
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Length", "1000")
		w.WriteHeader(http.StatusForbidden)
		_, _ = fmt.Fprint(w, "<Error>")
	}))
	t.Cleanup(server.Close)
	server.Start()
	d, c := probeInputs(server.URL)
	if got := probeRemote(context.Background(), d, c, true); got.State != RemoteNetworkUnavailable {
		t.Fatalf("interrupted body is not a valid provider refusal: %+v", got)
	}
}

func TestProbeRemoteDoesNotChangeCredentialAdmission(t *testing.T) {
	var calls atomic.Int64
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { calls.Add(1); _, _ = fmt.Fprint(w, probeListing("")) }))
	t.Cleanup(server.Close)
	server.Start()
	d, c := probeInputs(server.URL)
	c.ExpiresAt = time.Now().Add(25 * time.Second)
	// The short informational probe cannot relax activation's existing 30-second lifetime.
	if got := probeRemote(context.Background(), d, c, true); got.State != RemoteEmpty {
		t.Fatal(got)
	}
	err := verifyRemoteAccess(context.Background(), d, c, true)
	if err == nil || err.Error() != "restore test: credential_lifetime_insufficient" || calls.Load() != 1 {
		t.Fatalf("admission changed: %v calls=%d", err, calls.Load())
	}
	c.ExpiresAt = time.Time{}
	if err := verifyRemoteAccess(context.Background(), d, c, true); err != nil || calls.Load() != 2 {
		t.Fatal("empty listing no longer admits credentials", err)
	}
}
