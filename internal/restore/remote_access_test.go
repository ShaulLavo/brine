package restore

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestRemoteAccessUsesOneSignedReadOnlyEpochListing(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Method != "GET" || r.URL.Path != "/backup-bucket" || len(r.URL.Query()) != 3 || r.URL.Query().Get("list-type") != "2" || r.URL.Query().Get("max-keys") != "1" || r.URL.Query().Get("prefix") != "base/epochs/exact/" || r.Header.Get("X-Amz-Security-Token") != "fixture-session" {
			t.Error("unexpected listing request")
		}
		copy := r.Clone(r.Context())
		copy.Header = copy.Header.Clone()
		date, err := time.Parse("20060102T150405Z", r.Header.Get("X-Amz-Date"))
		if err != nil {
			t.Error(err)
		}
		// Re-signing a different scope must change authorization: the query is signed.
		copy.URL.RawQuery = "list-type=2&max-keys=1&prefix=foreign%2F"
		signGET(copy, "auto", Credentials{AccessKey: "fixture-access", SecretKey: "fixture-secret", SessionToken: "fixture-session"}, date)
		if copy.Header.Get("Authorization") == r.Header.Get("Authorization") {
			t.Error("query not signed")
		}
		_, _ = fmt.Fprint(w, `<ListBucketResult><Name>backup-bucket</Name><Prefix>base/epochs/exact/</Prefix><MaxKeys>1</MaxKeys><KeyCount>0</KeyCount></ListBucketResult>`)
	}))
	defer server.Close()
	d := Destination{Endpoint: server.URL, Region: "auto", Bucket: "backup-bucket", Prefix: "base/epochs/exact", PathStyle: true}
	c := Credentials{AccessKey: "fixture-access", SecretKey: "fixture-secret", SessionToken: "fixture-session", ReceivedAt: time.Now().Add(-time.Minute)}
	if err := verifyRemoteAccess(context.Background(), d, c, true); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatal("read proof retried")
	}
	if err := VerifyRemoteAccess(context.Background(), d, c); err == nil || calls != 1 {
		t.Fatal("production allowed HTTP")
	}
}
func TestRemoteAccessRefusesUnknownResponsesWithoutLeaksOrRetries(t *testing.T) {
	for _, test := range []struct {
		name   string
		status int
		body   string
	}{
		{"denied", 403, "PLANTED_SECRET"},
		{"redirect", 302, ""},
		{"oversized", 200, strings.Repeat("x", (64<<10)+1)},
		{"malformed", 200, "PLANTED_SECRET"},
		{"foreign", 200, `<ListBucketResult><Name>backup-bucket</Name><Prefix>foreign/</Prefix><MaxKeys>1</MaxKeys><KeyCount>0</KeyCount></ListBucketResult>`},
		{"trailing", 200, `<ListBucketResult><Name>backup-bucket</Name><Prefix>epoch/</Prefix><MaxKeys>1</MaxKeys><KeyCount>0</KeyCount></ListBucketResult><Extra/>`},
		{"foreign_object", 200, `<ListBucketResult><Name>backup-bucket</Name><Prefix>epoch/</Prefix><MaxKeys>1</MaxKeys><KeyCount>1</KeyCount><Contents><Key>epoch-foreign/file</Key></Contents></ListBucketResult>`},
	} {
		t.Run(test.name, func(t *testing.T) {
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				w.Header().Set("Location", "/redirect")
				w.WriteHeader(test.status)
				_, _ = fmt.Fprint(w, test.body)
			}))
			defer server.Close()
			err := verifyRemoteAccess(context.Background(), Destination{Endpoint: server.URL, Region: "auto", Bucket: "backup-bucket", Prefix: "epoch", PathStyle: true}, Credentials{AccessKey: "PLANTED_ACCESS", SecretKey: "PLANTED_SECRET", ReceivedAt: time.Now().Add(-time.Minute)}, true)
			if err == nil || calls != 1 || strings.Contains(err.Error(), "PLANTED") {
				t.Fatal("unsafe remote proof", err, calls)
			}
		})
	}
}
