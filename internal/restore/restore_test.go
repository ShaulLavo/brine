package restore

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func fixture(t *testing.T, statements string) []byte {
	t.Helper()
	path := filepath.Join(t.TempDir(), "fixture.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(statements)
	if err != nil {
		t.Fatal(err)
	}
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

type observer struct{ observation SchemaObservation }

func (o observer) Observe(context.Context, *sql.Tx) (SchemaObservation, error) {
	return o.observation, nil
}

func setup(t *testing.T, data []byte) (*Engine, Request, *int) {
	t.Helper()
	calls := new(int)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*calls++
		if r.Method != "GET" || r.URL.Path != "/bucket/epochs/e1/restore-points/p1/snapshot.sqlite" {
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		if !strings.HasPrefix(r.Header.Get("Authorization"), "AWS4-HMAC-SHA256 ") || r.Header.Get("X-Amz-Security-Token") != "test-session" {
			t.Error("missing signed session credentials")
		}
		w.Write(data)
	}))
	t.Cleanup(server.Close)
	digest := sha256.Sum256(data)
	schema := SchemaObservation{State: VerifiedSchema, Marker: "fixture-v1", CatalogSHA256: strings.Repeat("a", 64)}
	binding := Binding{ID: "b1", Epoch: "e1", CredentialRef: "c1", Destination: Destination{Endpoint: server.URL, Region: "test-region", Bucket: "bucket", Prefix: "epochs/e1", PathStyle: true}}
	root := t.TempDir()
	if err := os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	}
	e := &Engine{Root: root, AllowHTTPForTests: true, Bindings: BindingReaderFunc(func(context.Context, string, string) (Binding, error) { return binding, nil }), Credentials: CredentialReaderFunc(func(context.Context, string) (Credentials, error) {
		return Credentials{AccessKey: "test-key", SecretKey: "test-secret", SessionToken: "test-session"}, nil
	}), Observer: observer{schema}}
	request := Request{OperationID: "op1", Source: RestoreSource{Kind: SQLiteSnapshot, Snapshot: &SnapshotSource{BindingID: "b1", Epoch: "e1", PointID: "p1", ObjectKey: "epochs/e1/restore-points/p1/snapshot.sqlite", SHA256: hex.EncodeToString(digest[:]), Size: int64(len(data))}}, Budget: time.Minute, ExpectedSchema: schema, Sentinel: &Sentinel{Table: "fixture_commits", Sequence: 7, Marker: "marker-7", CommittedAt: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)}}
	return e, request, calls
}

const fixtureSQL = `CREATE TABLE fixture_commits(sequence INTEGER PRIMARY KEY, marker TEXT NOT NULL, committed_at TEXT NOT NULL); INSERT INTO fixture_commits VALUES(7,'marker-7','2026-01-02T03:04:05Z');`

func TestSnapshotIsolatedVerifiedReceipt(t *testing.T) {
	e, req, calls := setup(t, fixture(t, fixtureSQL))
	live := filepath.Join(t.TempDir(), "live.db")
	os.WriteFile(live, []byte("untouched"), 0600)
	receipt, err := e.Test(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if *calls != 1 || receipt.ToolVersion != SnapshotToolVersion || receipt.LossWindow.State != LossUnknown || receipt.Sentinel == nil || receipt.Sentinel.Marker != "marker-7" {
		t.Fatalf("receipt %+v", receipt)
	}
	for path, want := range map[string]os.FileMode{filepath.Dir(receipt.DatabasePath): 0700, receipt.DatabasePath: 0600} {
		info, err := os.Stat(path)
		if err != nil || info.Mode().Perm() != want {
			t.Fatalf("private mode %s %v %v", path, info, err)
		}
	}
	got, _ := os.ReadFile(live)
	if string(got) != "untouched" {
		t.Fatal("live data changed")
	}
	_, err = e.Test(context.Background(), req)
	if err == nil {
		t.Fatal("operation directory reused")
	}
}

func TestCredentialsMayBeIssuedDuringRead(t *testing.T) {
	e, req, calls := setup(t, fixture(t, fixtureSQL))
	e.Credentials = CredentialReaderFunc(func(context.Context, string) (Credentials, error) {
		return Credentials{AccessKey: "test-key", SecretKey: "test-secret", SessionToken: "test-session", ReceivedAt: time.Now().UTC(), ExpiresAt: time.Now().Add(2 * req.Budget)}, nil
	})
	if _, err := e.Test(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	if *calls != 1 {
		t.Fatal("freshly issued credentials did not reach snapshot download")
	}
}

func TestSnapshotRefusals(t *testing.T) {
	cases := []struct {
		name   string
		change func(*Engine, *Request)
	}{
		{"hash", func(_ *Engine, r *Request) { r.Source.Snapshot.SHA256 = strings.Repeat("0", 64) }},
		{"size", func(_ *Engine, r *Request) { r.Source.Snapshot.Size-- }},
		{"prefix", func(_ *Engine, r *Request) { r.Source.Snapshot.ObjectKey = "foreign/snapshot.sqlite" }},
		{"union", func(_ *Engine, r *Request) { r.Source.LTX = &LTXSource{} }},
		{"expired", func(e *Engine, _ *Request) {
			e.Credentials = CredentialReaderFunc(func(context.Context, string) (Credentials, error) {
				return Credentials{AccessKey: "test-key", SecretKey: "test-secret", ExpiresAt: time.Now().Add(time.Second)}, nil
			})
		}},
		{"schema", func(_ *Engine, r *Request) { r.ExpectedSchema.Marker = "different" }},
		{"sentinel", func(_ *Engine, r *Request) { r.Sentinel.Marker = "wrong" }},
		{"supplied SQL", func(_ *Engine, r *Request) {
			r.Invariants = []Invariant{{Kind: RowCount, Table: "fixture_commits; DROP TABLE fixture_commits", Count: 1}}
		}},
		{"invariant", func(_ *Engine, r *Request) {
			r.Invariants = []Invariant{{Kind: RowCount, Table: "fixture_commits", Count: 2}}
		}},
		{"unknown schema", func(e *Engine, _ *Request) { e.Observer = observer{SchemaObservation{State: Unknown}} }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e, r, _ := setup(t, fixture(t, fixtureSQL))
			tc.change(e, &r)
			if _, err := e.Test(context.Background(), r); err == nil {
				t.Fatal("accepted invalid restore")
			}
		})
	}
}

func TestVerificationCorruptionAndForeignKeys(t *testing.T) {
	for name, data := range map[string][]byte{"corrupt": []byte("not sqlite"), "foreign keys": fixture(t, `CREATE TABLE parent(id INTEGER PRIMARY KEY); CREATE TABLE child(id INTEGER REFERENCES parent(id)); INSERT INTO child VALUES(9);`)} {
		t.Run(name, func(t *testing.T) {
			e, r, _ := setup(t, data)
			r.Sentinel = nil
			if _, err := e.Test(context.Background(), r); err == nil {
				t.Fatal("accepted invalid database")
			}
		})
	}
}

type fakeCLI struct {
	t        *testing.T
	data     []byte
	txid     string
	commands []Command
}

func (f *fakeCLI) Execute(_ context.Context, c Command) (CommandResult, error) {
	f.commands = append(f.commands, c)
	if len(c.Args) == 1 && c.Args[0] == "version" {
		data, err := os.ReadFile("testdata/litestream-version.txt")
		if err != nil {
			f.t.Fatal(err)
		}
		return CommandResult{Stdout: data}, nil
	}
	args := strings.Join(c.Args, " ")
	if !strings.Contains(args, "-txid 0000000000000007") || !strings.Contains(args, "-integrity-check full") || !strings.Contains(args, "-no-expand-env") {
		f.t.Fatalf("unsafe args %v", c.Args)
	}
	var output string
	for i, arg := range c.Args {
		if arg == "-o" {
			output = c.Args[i+1]
		}
	}
	if strings.Contains(args, "-dry-run") {
		result, _ := json.Marshal(map[string]string{"target_path": output, "replica": "s3", "max_txid": "0000000000000007"})
		return CommandResult{Stdout: result}, nil
	}
	if err := os.WriteFile(output, f.data, 0600); err != nil {
		f.t.Fatal(err)
	}
	result, _ := json.Marshal(map[string]string{"db_path": output, "replica": "s3", "txid": f.txid, "integrity_check": "full"})
	return CommandResult{Stdout: result}, nil
}

func TestLTXExactPositionAndBarrier(t *testing.T) {
	e, r, calls := setup(t, fixture(t, fixtureSQL))
	cli := &fakeCLI{t: t, data: fixture(t, fixtureSQL), txid: "0000000000000007"}
	e.CLI = cli
	r.Source = RestoreSource{Kind: LitestreamLTX, LTX: &LTXSource{BindingID: "b1", Epoch: "e1", TXID: 7, Barrier: &BarrierReceipt{BindingID: "b1", Epoch: "e1", TXID: 7, ReplicaTXID: 8, ObservedAt: time.Now().Add(-time.Second), Succeeded: true}}}
	receipt, err := e.Test(context.Background(), r)
	if err != nil {
		t.Fatal(err)
	}
	if receipt.RequestedTXID != 7 || receipt.RecoveredTXID != 7 || receipt.Barrier == nil || *calls != 0 || len(cli.commands) != 3 {
		t.Fatalf("receipt %+v commands %d", receipt, len(cli.commands))
	}
	for _, c := range cli.commands {
		if strings.Contains(strings.Join(c.Args, " "), "test-secret") {
			t.Fatal("credential in argv")
		}
	}
}

func TestLTXRefusesWrongRecoveredPosition(t *testing.T) {
	e, r, _ := setup(t, fixture(t, fixtureSQL))
	e.CLI = &fakeCLI{t: t, data: fixture(t, fixtureSQL), txid: "0000000000000008"}
	r.Source = RestoreSource{Kind: LitestreamLTX, LTX: &LTXSource{BindingID: "b1", Epoch: "e1", TXID: 7}}
	if _, err := e.Test(context.Background(), r); err == nil {
		t.Fatal("accepted wrong TXID")
	}
}
