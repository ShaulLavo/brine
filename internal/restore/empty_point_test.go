package restore

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestEmptySnapshotCreateOnlyAndIndependentRestore(t *testing.T) {
	var payload []byte
	var puts, gets int
	var capabilityObserved atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/bucket/epochs/e1/restore-points/p1/snapshot.sqlite" {
			t.Errorf("wrong key %s", r.URL.Path)
		}
		if !strings.Contains(r.Header.Get("Authorization"), "AWS4-HMAC-SHA256") || r.Header.Get("X-Amz-Security-Token") != "test-session" {
			t.Error("unsigned credentials")
		}
		switch r.Method {
		case http.MethodPut:
			puts++
			if r.Header.Get("If-None-Match") != "*" || !strings.Contains(r.Header.Get("Authorization"), "if-none-match") {
				t.Error("unsigned create-only condition")
			}
			if payload != nil {
				capabilityObserved.Store(time.Now().UnixNano())
				w.WriteHeader(http.StatusPreconditionFailed)
				return
			}
			var err error
			payload, err = io.ReadAll(r.Body)
			if err != nil {
				t.Error(err)
			}
			w.WriteHeader(http.StatusCreated)
		case http.MethodGet:
			gets++
			_, _ = w.Write(payload)
		default:
			t.Error("unexpected request")
		}
	}))
	defer server.Close()
	binding := Binding{ID: "b1", Epoch: "e1", CredentialRef: "c1", Destination: Destination{Endpoint: server.URL, Region: "test-region", Bucket: "bucket", Prefix: "epochs/e1", PathStyle: true}}
	credentials := Credentials{AccessKey: "test-key", SecretKey: "test-secret", SessionToken: "test-session"}
	root := t.TempDir()
	if err := os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	} // #nosec G302 -- Private directory requires owner traversal.

	source, uploaded, err := CreateEmptySnapshot(context.Background(), root, binding, credentials, "p1", time.Minute, true)
	if err != nil {
		t.Fatal(err)
	}
	if !uploaded.Before(time.Unix(0, capabilityObserved.Load())) {
		t.Fatal("capability check refreshed the upload age")
	}
	engine := Engine{Root: root, AllowHTTPForTests: true, MaxSnapshotBytes: 1 << 20, Observer: EmptySchemaObserver{}, Bindings: BindingReaderFunc(func(context.Context, string, string) (Binding, error) { return binding, nil }), Credentials: CredentialReaderFunc(func(context.Context, string) (Credentials, error) { return credentials, nil })}
	request := Request{OperationID: "empty-proof", Source: RestoreSource{Kind: SQLiteSnapshot, Snapshot: &source}, Budget: time.Minute, ExpectedSchema: SchemaObservation{State: VerifiedEmpty, Marker: "brine-empty-v1", CatalogSHA256: "4f53cda18c2baa0c0354bb5f9a3ecbe5ed12ab4d8e11ba873c2f11161202b945"}}
	receipt, err := engine.Test(context.Background(), request)
	if err != nil || receipt.ObservedAt.Before(uploaded) || receipt.IntegrityCheck != "passed" || receipt.ForeignKeyCheck != "passed" || puts != 2 || gets != 1 {
		t.Fatalf("proof %+v puts=%d gets=%d err=%v", receipt, puts, gets, err)
	}
	// A colliding point is refused; it is not overwritten or retried.
	if _, _, err = CreateEmptySnapshot(context.Background(), root, binding, credentials, "p1", time.Minute, true); err == nil || puts != 3 {
		t.Fatalf("collision replayed: puts=%d err=%v", puts, err)
	}
}

func TestEmptySnapshotCapabilityFailuresNeverRetry(t *testing.T) {
	for _, scenario := range []string{"unsupported", "collision", "denied", "unknown", "expired"} {
		t.Run(scenario, func(t *testing.T) {
			var calls atomic.Int64
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				switch scenario {
				case "unsupported":
					w.WriteHeader(http.StatusCreated)
				case "collision":
					w.WriteHeader(http.StatusPreconditionFailed)
				case "denied":
					w.WriteHeader(http.StatusForbidden)
				case "unknown":
					conn, _, err := w.(http.Hijacker).Hijack()
					if err != nil {
						t.Error(err)
						return
					}
					_ = conn.Close()
				default:
					t.Error("unexpected request")
				}
			}))
			defer server.Close()
			root := t.TempDir()
			if err := os.Chmod(root, 0700); err != nil {
				t.Fatal(err)
			} // #nosec G302 -- Private directory requires owner traversal.

			b := Binding{ID: "b1", Epoch: "e1", CredentialRef: "c1", Destination: Destination{Endpoint: server.URL, Region: "test-region", Bucket: "bucket", Prefix: "epochs/e1", PathStyle: true}}
			c := Credentials{AccessKey: "test-key", SecretKey: "test-secret"}
			if scenario == "expired" {
				c.ExpiresAt = time.Now().Add(-time.Minute)
			}
			_, _, err := CreateEmptySnapshot(context.Background(), root, b, c, "p1", time.Minute, true)
			want := 1
			if scenario == "unsupported" {
				want = 2
			}
			if scenario == "expired" {
				want = 0
			}
			if err == nil || calls.Load() != int64(want) {
				t.Fatalf("unsafe result calls=%d want=%d err=%v", calls.Load(), want, err)
			}
		})
	}
}

func TestEmptySchemaObserverRefusesApplicationData(t *testing.T) {
	e, r, _ := setup(t, fixture(t, fixtureSQL))
	e.Observer = EmptySchemaObserver{}
	r.Sentinel = nil
	r.ExpectedSchema = SchemaObservation{State: VerifiedEmpty, Marker: "brine-empty-v1", CatalogSHA256: "4f53cda18c2baa0c0354bb5f9a3ecbe5ed12ab4d8e11ba873c2f11161202b945"}
	if _, err := e.Test(context.Background(), r); err == nil {
		t.Fatal("nonempty restore accepted")
	}
}
