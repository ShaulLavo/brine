package restore

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
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
	// #nosec G302 -- Private fixture directory requires owner traversal.
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
	engine := SQLiteEngine{Root: root, AllowHTTPForTests: true, MaxSnapshotBytes: 1 << 20, Observer: EmptySchemaObserver{}, Bindings: BindingReaderFunc(func(context.Context, string, string) (Binding, error) { return binding, nil }), Credentials: CredentialReaderFunc(func(context.Context, string) (Credentials, error) { return credentials, nil })}
	request := SQLiteRequest{OperationID: "empty-proof", Source: SQLiteSource{Kind: SQLiteSnapshot, Snapshot: &source}, Budget: time.Minute, ExpectedSchema: SQLiteSchemaObservation{State: VerifiedEmpty, Marker: "brine-empty-v1", CatalogSHA256: "4f53cda18c2baa0c0354bb5f9a3ecbe5ed12ab4d8e11ba873c2f11161202b945"}}
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
			// #nosec G302 -- Private fixture directory requires owner traversal.
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
	r.ExpectedSchema = SQLiteSchemaObservation{State: VerifiedEmpty, Marker: "brine-empty-v1", CatalogSHA256: "4f53cda18c2baa0c0354bb5f9a3ecbe5ed12ab4d8e11ba873c2f11161202b945"}
	if _, err := e.Test(context.Background(), r); err == nil {
		t.Fatal("nonempty restore accepted")
	}
}

func TestInitializedSnapshotUploadAndIndependentRemoteCoverage(t *testing.T) {
	raw := fixture(t, fixtureSQL)
	var stored []byte
	var methods []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		methods = append(methods, r.Method)
		if r.URL.Path != "/bucket/epochs/e1/restore-points/p1/snapshot.sqlite" {
			t.Error("unexpected object", r.URL.Path)
		}
		switch r.Method {
		case http.MethodPut:
			if r.Header.Get("If-None-Match") != "*" {
				t.Error("upload can overwrite")
			}
			if stored != nil {
				w.WriteHeader(http.StatusPreconditionFailed)
				return
			}
			var err error
			stored, err = io.ReadAll(r.Body)
			if err != nil {
				t.Error(err)
			}
			w.WriteHeader(http.StatusCreated)
		case http.MethodGet:
			if _, err := w.Write(stored); err != nil {
				t.Error(err)
			}
		default:
			t.Error("unexpected provider operation")
		}
	}))
	defer server.Close()
	binding := Binding{ID: "b1", Epoch: "e1", CredentialRef: "c1", Destination: Destination{Endpoint: server.URL, Region: "test-region", Bucket: "bucket", Prefix: "epochs/e1", PathStyle: true}}
	credentials := Credentials{AccessKey: "test-key", SecretKey: "test-secret"}
	cutoff := time.Now().UTC()
	source, _, err := UploadSnapshot(context.Background(), binding, credentials, "p1", raw, time.Minute, true)
	if err != nil {
		t.Fatal(err)
	}
	clear(raw) // The independent read must use the stored remote copy, not upload bytes.
	root := t.TempDir()
	// #nosec G302 -- Private fixture directory requires owner traversal.
	if err = os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	}
	schema := SQLiteSchemaObservation{State: VerifiedSchema, Marker: "fixture-v1", CatalogSHA256: strings.Repeat("a", 64)}
	engine := SQLiteEngine{Root: root, AllowHTTPForTests: true, Observer: observer{schema}, Bindings: BindingReaderFunc(func(context.Context, string, string) (Binding, error) { return binding, nil }), Credentials: CredentialReaderFunc(func(context.Context, string) (Credentials, error) { return credentials, nil })}
	request := SQLiteRequest{OperationID: "archive-coverage", Source: SQLiteSource{Kind: SQLiteSnapshot, Snapshot: &source}, Budget: time.Minute, ExpectedSchema: schema, Invariants: []Invariant{{Kind: RowCount, Table: "fixture_commits", Count: 1}}}
	request.Coverage = &CoverageEvidence{Source: request.Source, CoveredThrough: cutoff}
	receipt, err := engine.Test(context.Background(), request)
	if err != nil || !receipt.Valid() || receipt.LossWindow.State != LossBounded || receipt.InvariantCheck != "passed" || strings.Join(methods, ",") != "PUT,PUT,GET" {
		t.Fatal("remote coverage failed", receipt, err, methods)
	}
}

// The fake destination atomically checks and creates, like If-None-Match: *.
func TestUploadSnapshotEightWayCompetition(t *testing.T) {
	var mutex sync.Mutex
	var winner []byte
	var requests, creates, rejected int
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, err := io.ReadAll(r.Body)
		if err != nil {
			t.Error(err)
			return
		}
		mutex.Lock()
		defer mutex.Unlock()
		requests++
		if r.Method != http.MethodPut || r.URL.Path != "/bucket/epochs/e1/restore-points/shared/snapshot.sqlite" || r.Header.Get("If-None-Match") != "*" || !strings.Contains(r.Header.Get("Authorization"), "if-none-match") {
			t.Error("nonconditional or foreign upload")
		}
		if winner != nil {
			rejected++
			w.WriteHeader(http.StatusPreconditionFailed)
			return
		}
		winner = bytes.Clone(raw)
		creates++
		w.WriteHeader(http.StatusCreated)
	}))
	t.Cleanup(server.Close)
	server.Start()
	binding := Binding{ID: "b1", Epoch: "e1", CredentialRef: "c1", Destination: Destination{Endpoint: server.URL, Region: "auto", Bucket: "bucket", Prefix: "epochs/e1", PathStyle: true}}
	credentials := Credentials{AccessKey: "test-key", SecretKey: "test-secret"}
	start := make(chan struct{})
	type result struct {
		raw      []byte
		source   SnapshotSource
		uploaded time.Time
		err      error
	}
	results := make(chan result, 8)
	var workers sync.WaitGroup
	// Cleanup is installed before launching any contender.
	t.Cleanup(workers.Wait)
	for i := 0; i < 8; i++ {
		workers.Add(1)
		go func(id int) {
			defer workers.Done()
			raw := []byte(fmt.Sprintf("SQLite format 3\x00contender-%d", id))
			<-start
			source, uploaded, err := UploadSnapshot(context.Background(), binding, credentials, "shared", raw, time.Minute, true)
			results <- result{raw, source, uploaded, err}
		}(i)
	}
	close(start)
	workers.Wait()
	close(results)
	mutex.Lock()
	defer mutex.Unlock()
	successes := 0
	for got := range results {
		if got.err != nil {
			continue
		}
		successes++
		digest := sha256.Sum256(got.raw)
		if !bytes.Equal(winner, got.raw) || got.source.SHA256 != hex.EncodeToString(digest[:]) || got.source.Size != int64(len(winner)) || got.uploaded.IsZero() {
			t.Fatal("winner bytes or receipt changed")
		}
	}
	if successes != 1 || creates != 1 || requests != 9 || rejected != 8 {
		t.Fatalf("successes=%d creates=%d requests=%d rejected=%d", successes, creates, requests, rejected)
	}
}

func TestUploadSnapshotUnknownReplyEndsInvocation(t *testing.T) {
	for _, unknownAt := range []int64{1, 2} {
		t.Run(fmt.Sprint(unknownAt), func(t *testing.T) { uploadUnknownReply(t, unknownAt) })
	}
}

func uploadUnknownReply(t *testing.T, unknownAt int64) {
	t.Helper()
	var calls atomic.Int64
	var mutex sync.Mutex
	var winner []byte
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		call := calls.Add(1)
		if r.Method != http.MethodPut || r.Header.Get("If-None-Match") != "*" || !strings.Contains(r.Header.Get("Authorization"), "if-none-match") {
			t.Error("nonconditional upload")
		}
		raw, err := io.ReadAll(r.Body)
		if err != nil {
			t.Error(err)
			return
		}
		mutex.Lock()
		defer mutex.Unlock()
		if call == 1 {
			winner = bytes.Clone(raw)
		}
		if !bytes.Equal(raw, winner) {
			t.Error("capability check changed bytes")
		}
		if call == unknownAt {
			closeUnknownReply(t, w)
			return
		}
		w.WriteHeader(http.StatusCreated)
	}))
	t.Cleanup(server.Close)
	server.Start()
	binding := Binding{ID: "b1", Epoch: "e1", CredentialRef: "c1", Destination: Destination{Endpoint: server.URL, Region: "auto", Bucket: "bucket", Prefix: "epochs/e1", PathStyle: true}}
	raw := []byte("SQLite format 3\x00unchanged")
	_, uploaded, err := UploadSnapshot(context.Background(), binding, Credentials{AccessKey: "key", SecretKey: "secret"}, "point", raw, time.Minute, true)
	mutex.Lock()
	defer mutex.Unlock()
	if err == nil || err.Error() != "restore test: snapshot_upload_unknown" || !uploaded.IsZero() || calls.Load() != unknownAt || !bytes.Equal(winner, raw) {
		t.Fatalf("unknown reply repeated or measured: calls=%d uploaded=%v err=%v", calls.Load(), uploaded, err)
	}
}

func closeUnknownReply(t *testing.T, w http.ResponseWriter) {
	t.Helper()
	conn, _, err := w.(http.Hijacker).Hijack()
	if err != nil {
		t.Error(err)
		return
	}
	if err := conn.Close(); err != nil {
		t.Error(err)
	}
}
