package restore

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestDeclaredChecks(t *testing.T) {
	for _, check := range []Invariant{{Kind: RowCount, Table: "fixture_commits", Count: 1}, {Kind: NonNull, Table: "fixture_commits", Column: "marker"}, {Kind: IntegerRange, Table: "fixture_commits", Column: "sequence", Minimum: 1, Maximum: 7}} {
		e, r, _ := setup(t, fixture(t, fixtureSQL))
		r.OperationID = "valid-check"
		r.Invariants = []Invariant{check}
		receipt, err := e.Test(context.Background(), r)
		if err != nil || receipt.InvariantCheck != "passed" {
			t.Fatalf("typed check %v %v", check, err)
		}
	}
	for _, check := range []Invariant{{Kind: NonNull, Table: "fixture_commits", Column: "missing"}, {Kind: IntegerRange, Table: "fixture_commits", Column: "sequence", Minimum: 8, Maximum: 10}, {Kind: NonNull, Table: "missing", Column: "marker"}, {Kind: IntegerRange, Table: "fixture_commits", Column: "marker", Minimum: 1, Maximum: 8}} {
		e, r, _ := setup(t, fixture(t, fixtureSQL))
		r.Invariants = []Invariant{check}
		if _, err := e.Test(context.Background(), r); err == nil {
			t.Fatalf("bad check accepted %v", check)
		}
	}
	e, r, _ := setup(t, fixture(t, fixtureSQL))
	r.Sentinel = nil
	receipt, err := e.Test(context.Background(), r)
	if err != nil || receipt.InvariantCheck != "unknown" {
		t.Fatalf("unsupported invariants not unknown %v", err)
	}
}

func TestAffirmativeEmptySnapshot(t *testing.T) {
	schema := SchemaObservation{State: VerifiedEmpty, Marker: "brine-empty-v1", CatalogSHA256: "4f53cda18c2baa0c0354bb5f9a3ecbe5ed12ab4d8e11ba873c2f11161202b945"}
	e, r, _ := setup(t, fixture(t, "PRAGMA user_version=0"))
	e.Observer = gateSchemaObserver{}
	r.ExpectedSchema = schema
	r.Sentinel = nil
	receipt, err := e.Test(context.Background(), r)
	if err != nil || receipt.Schema != schema || receipt.InvariantCheck != "passed" {
		t.Fatalf("empty proof %v %+v", err, receipt)
	}
	e, r, _ = setup(t, fixture(t, fixtureSQL))
	e.Observer = gateSchemaObserver{}
	r.ExpectedSchema = schema
	r.Sentinel = nil
	if _, err := e.Test(context.Background(), r); err == nil {
		t.Fatal("nonempty snapshot accepted as empty")
	}
}

type mutationObserver struct{}

func (mutationObserver) Observe(ctx context.Context, tx *sql.Tx) (SchemaObservation, error) {
	_, err := tx.ExecContext(ctx, "DELETE FROM fixture_commits")
	return SchemaObservation{}, err
}
func TestObserverCannotWrite(t *testing.T) {
	e, r, _ := setup(t, fixture(t, fixtureSQL))
	e.Observer = mutationObserver{}
	if _, err := e.Test(context.Background(), r); err == nil {
		t.Fatal("read-only observer mutation accepted")
	}
}

func TestPrivateWorkspaceAndOutput(t *testing.T) {
	root := t.TempDir()
	if _, err := newDirectory(root, "op"); err == nil {
		t.Fatal("public root accepted")
	}
	if err := os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(root, link); err != nil {
		t.Fatal(err)
	}
	if _, err := newDirectory(link, "op"); err == nil {
		t.Fatal("symlink workspace accepted")
	}
	private, err := newDirectory(root, "op")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(private, "database.sqlite")
	os.WriteFile(path, []byte("data"), 0600)
	os.WriteFile(path+"-wal", []byte("wal"), 0600)
	if err := validateOutput(path); err == nil {
		t.Fatal("WAL output sibling accepted")
	}
	os.Remove(path + "-wal")
	os.Remove(path)
	os.Symlink(filepath.Join(t.TempDir(), "foreign"), path)
	if err := validateOutput(path); err == nil {
		t.Fatal("symlink output accepted")
	}
}

func TestSourceAndBindingRefusals(t *testing.T) {
	for _, source := range []RestoreSource{{Kind: "file"}, {Kind: LitestreamLTX, LTX: &LTXSource{TXID: 0}}, {Kind: SQLiteSnapshot, Snapshot: &SnapshotSource{PointID: "../escape"}}, {Kind: LitestreamLTX, LTX: &LTXSource{BindingID: "b1", Epoch: "e1", TXID: 7, Barrier: &BarrierReceipt{BindingID: "b1", Epoch: "e1", TXID: 7, ReplicaTXID: 6, ObservedAt: time.Now(), Succeeded: true}}}} {
		if _, _, err := source.reference(); err == nil {
			t.Fatal("invalid tagged source accepted")
		}
	}
	e, r, calls := setup(t, fixture(t, fixtureSQL))
	e.Bindings = BindingReaderFunc(func(context.Context, string, string) (Binding, error) {
		return Binding{ID: "another", Epoch: "e1"}, nil
	})
	if _, err := e.Test(context.Background(), r); err == nil || *calls != 0 {
		t.Fatal("binding identity not enforced")
	}
	e, r, calls = setup(t, fixture(t, fixtureSQL))
	e.Credentials = CredentialReaderFunc(func(context.Context, string) (Credentials, error) { return Credentials{}, errors.New("private-value") })
	_, err := e.Test(context.Background(), r)
	if err == nil || strings.Contains(err.Error(), "private-value") || *calls != 0 {
		t.Fatal("credential boundary leaked")
	}
	e, r, _ = setup(t, fixture(t, fixtureSQL))
	r.Coverage = &CoverageEvidence{Source: r.Source, CoveredThrough: time.Now().Add(-time.Minute)}
	receipt, err := e.Test(context.Background(), r)
	if err != nil || receipt.LossWindow.State != LossBounded || !receipt.LossWindow.From.Before(receipt.LossWindow.To) {
		t.Fatal("nonzero bounded loss window missing")
	}
}

func TestCredentialsRedactionAndLifetime(t *testing.T) {
	secret := Credentials{AccessKey: "private-key", SecretKey: "private-secret", SessionToken: "private-token"}
	encoded, _ := json.Marshal(secret)
	for _, text := range []string{string(encoded), fmt.Sprintf("%v", secret), fmt.Sprintf("%+v", secret), fmt.Sprintf("%#v", secret)} {
		if strings.Contains(text, "private-") {
			t.Fatal("credential serialization leaked")
		}
	}
	now := time.Now()
	for _, credentials := range []Credentials{{AccessKey: "k", SecretKey: "s", ExpiresAt: now.Add(time.Minute)}, {AccessKey: "k", SecretKey: "s\n"}, {AccessKey: "k", SecretKey: "s", ReceivedAt: now.Add(time.Second)}} {
		if err := validateCredentials(credentials, now, time.Minute); err == nil {
			t.Fatal("invalid credential admission")
		}
	}
}

func TestBoundedConcurrentCLIOutput(t *testing.T) {
	output := &boundedBuffer{}
	var group sync.WaitGroup
	for range 4 {
		group.Go(func() {
			for range 4 {
				data := bytes.Repeat([]byte("x"), 32<<10)
				count, err := output.Write(data)
				if err != nil || count != len(data) {
					t.Error("bounded write contract")
				}
			}
		})
	}
	group.Wait()
	data, truncated := output.snapshot()
	if !truncated || len(data) != 64<<10 {
		t.Fatal("output not bounded")
	}
	if _, err := (ExecCLI{}).Execute(context.Background(), Command{Directory: t.TempDir()}); err == nil {
		t.Fatal("unbounded CLI accepted")
	}
}

func TestSnapshotHTTPBoundaries(t *testing.T) {
	for _, name := range []string{"redirect", "denied", "oversize", "timeout"} {
		t.Run(name, func(t *testing.T) {
			redirected := false
			foreign := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { redirected = true }))
			defer foreign.Close()
			endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch name {
				case "redirect":
					http.Redirect(w, r, foreign.URL, http.StatusTemporaryRedirect)
				case "denied":
					w.WriteHeader(http.StatusForbidden)
					fmt.Fprint(w, "private-value")
				case "oversize":
					w.(http.Flusher).Flush()
					fmt.Fprint(w, "oversized-body")
				case "timeout":
					<-r.Context().Done()
				}
			}))
			defer endpoint.Close()
			budget := time.Second
			if name == "timeout" {
				budget = 50 * time.Millisecond
			}
			ctx, cancel := context.WithTimeout(context.Background(), budget)
			defer cancel()
			err := downloadSnapshot(ctx, Destination{Endpoint: endpoint.URL, Region: "auto", Bucket: "bucket", Prefix: "p", PathStyle: true}, Credentials{AccessKey: "test-key", SecretKey: "test-secret"}, SnapshotSource{ObjectKey: "p/object", Size: 2, SHA256: strings.Repeat("0", 64)}, filepath.Join(t.TempDir(), "db"), true)
			if err == nil || redirected || strings.Contains(err.Error(), "private-value") {
				t.Fatal("unsafe HTTP boundary")
			}
		})
	}
}

func TestStructuredConfigHasNoCredentialExpansion(t *testing.T) {
	destination := Destination{Endpoint: "https://storage.example", Region: "auto", Bucket: "bucket", Prefix: "epoch/prefix", PathStyle: true}
	config := string(renderConfig("/private/selector.sqlite", destination))
	if !strings.HasPrefix(config, "logging:\n  stderr: true\n") {
		t.Fatal("config reload sends pinned CLI logs to stdout instead of preserving JSON")
	}
	if strings.Contains(config, "access-key") || strings.Contains(config, "secret") || strings.Contains(config, "s3://") || !strings.Contains(config, "replica:\n") || !strings.Contains(config, "force-path-style: true") {
		t.Fatal("config credential or topology violation")
	}
	for _, endpoint := range []string{"file:///replica", "http://storage.example", "https://user:password@storage.example", "https://storage.example?credential=private"} {
		destination.Endpoint = endpoint
		if err := validateDestination(destination, false); err == nil {
			t.Fatal("invalid destination accepted")
		}
	}
}

func TestPinnedPositionFixtures(t *testing.T) {
	syncJSON, err := os.ReadFile("testdata/litestream-sync.json")
	if err != nil {
		t.Fatal(err)
	}
	restoreJSON, err := os.ReadFile("testdata/litestream-restore.json")
	if err != nil {
		t.Fatal(err)
	}
	var barrier struct {
		TXID        uint64 `json:"txid"`
		ReplicaTXID uint64 `json:"replica_txid"`
	}
	var restored struct {
		TXID      string `json:"txid"`
		Replica   string `json:"replica"`
		Integrity string `json:"integrity_check"`
	}
	if err := json.Unmarshal(syncJSON, &barrier); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(restoreJSON, &restored); err != nil {
		t.Fatal(err)
	}
	if barrier.TXID != 1 || barrier.ReplicaTXID < barrier.TXID || restored.TXID != txidString(barrier.TXID) || restored.Replica != "s3" || restored.Integrity != "full" {
		t.Fatal("pinned numeric barrier and hex restore position differ")
	}
}

func TestGateFixtureSchemaObserver(t *testing.T) {
	path := filepath.Join(t.TempDir(), "fixture.sqlite")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ddl, err := os.ReadFile("testdata/fixture.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(string(ddl)); err != nil {
		t.Fatal(err)
	}
	rows := [][4]any{{"table", "fixture_commits", "fixture_commits", "CREATE TABLE fixture_commits(sequence INTEGER PRIMARY KEY, marker TEXT NOT NULL, committed_at TEXT NOT NULL)"}}
	data, _ := json.Marshal(rows)
	digest := sha256.Sum256(data)
	expected := SchemaObservation{State: VerifiedSchema, Marker: "fixture-v1", CatalogSHA256: hex.EncodeToString(digest[:])}
	committed := time.Now().UTC().Truncate(time.Second)
	if _, err := db.Exec(`INSERT INTO brine_schema_marker VALUES(1,?,?);`, expected.Marker, expected.CatalogSHA256); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO fixture_commits VALUES(7,'marker-7',?);`, committed.Format(time.RFC3339)); err != nil {
		t.Fatal(err)
	}
	_, sentinel, err := verify(context.Background(), path, gateSchemaObserver{}, Request{ExpectedSchema: expected, Sentinel: &Sentinel{Table: "fixture_commits", Sequence: 7, Marker: "marker-7", CommittedAt: committed}})
	if err != nil || sentinel == nil {
		t.Fatalf("gate fixture verification %v", err)
	}
}

func TestReceiptOwnsSourceIdentity(t *testing.T) {
	engine, request, _ := setup(t, fixture(t, fixtureSQL))
	receipt, err := engine.Test(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	original := receipt.Source.Snapshot.ObjectKey
	request.Source.Snapshot.ObjectKey = "another/object"
	if receipt.Source.Snapshot.ObjectKey != original {
		t.Fatal("receipt source identity follows caller mutation")
	}
}

func TestD12MarkerGrammar(t *testing.T) {
	for _, marker := range []string{"V1", "1_initial.schema", "v" + strings.Repeat("a", 127)} {
		if !validSchema(SchemaObservation{State: VerifiedSchema, Marker: marker, CatalogSHA256: strings.Repeat("a", 64)}) {
			t.Fatal("D12-valid marker rejected")
		}
	}
}

func TestCoverageCannotDescribeAnotherRestore(t *testing.T) {
	e, r, _ := setup(t, fixture(t, fixtureSQL))
	unrelated := *r.Source.Snapshot
	unrelated.Epoch = "another-epoch"
	r.Coverage = &CoverageEvidence{Source: RestoreSource{Kind: SQLiteSnapshot, Snapshot: &unrelated}, CoveredThrough: time.Now().Add(-time.Minute)}
	if _, err := e.Test(context.Background(), r); err == nil {
		t.Fatal("coverage for another source accepted")
	}
}

func TestOperatorDrillRejectsInputBeyondByteBound(t *testing.T) {
	input := `{"endpoint":"https://storage.example","region":"auto","bucket":"brine-test","prefix":"p04-05-gate/test-run/","access_key":"test-key","secret_key":"test-secret"}` + strings.Repeat(" ", 64<<10)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestRealStorageRestore$", "-test.v")
	command.Env = append(os.Environ(), "BRINE_RESTORE_GATE=1", "BRINE_LITESTREAM_GATE_BINARY=")
	command.Stdin = strings.NewReader(input)
	output := &boundedBuffer{}
	command.Stdout = output
	command.Stderr = output
	if err := command.Run(); err == nil {
		t.Fatal("oversized input accepted")
	}
	text, _ := output.snapshot()
	if !bytes.Contains(text, []byte("drill stdin bound exceeded")) {
		t.Fatalf("wrong refusal %s", text)
	}
}
