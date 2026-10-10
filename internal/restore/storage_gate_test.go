package restore

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"
)

// This opt-in drill is operator-driven. Its binary path and scoped credentials
// are not part of production admission or the ordinary unit test environment.
func TestRealStorageRestore(t *testing.T) {
	if os.Getenv("BRINE_RESTORE_GATE") != "1" {
		t.Skip("operator-scoped real storage drill not requested")
	}
	var input struct {
		Endpoint     string    `json:"endpoint"`
		Region       string    `json:"region"`
		Bucket       string    `json:"bucket"`
		Prefix       string    `json:"prefix"`
		AccessKey    string    `json:"access_key"`
		SecretKey    string    `json:"secret_key"`
		SessionToken string    `json:"session_token"`
		ExpiresAt    time.Time `json:"expires_at"`
	}
	inputBytes, err := io.ReadAll(io.LimitReader(os.Stdin, (32<<10)+1))
	if err != nil || len(inputBytes) > 32<<10 {
		t.Fatal("drill stdin bound exceeded")
	}
	decoder := json.NewDecoder(bytes.NewReader(inputBytes))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil {
		t.Fatal("invalid bounded drill input")
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		t.Fatal("unexpected trailing drill input")
	}
	prefix := strings.TrimSuffix(input.Prefix, "/")
	if !strings.HasPrefix(prefix, "p04-05-gate/") || !validKey(prefix) || input.Bucket != "brine-test" {
		t.Fatal("drill requires its authorized test bucket and prefix")
	}
	nonce := make([]byte, 8)
	if _, err := rand.Read(nonce); err != nil {
		t.Fatal("drill identity unavailable")
	}
	prefix += "/drill-" + hex.EncodeToString(nonce)
	destination := Destination{Endpoint: input.Endpoint, Region: input.Region, Bucket: input.Bucket, Prefix: prefix, PathStyle: true}
	if err := validateDestination(destination, false); err != nil {
		t.Fatal(err)
	}
	credentials := Credentials{AccessKey: input.AccessKey, SecretKey: input.SecretKey, SessionToken: input.SessionToken, ReceivedAt: time.Now().UTC(), ExpiresAt: input.ExpiresAt}
	if err := validateCredentials(credentials, time.Now(), 5*time.Minute); err != nil {
		t.Fatal(err)
	}
	binary := os.Getenv("BRINE_LITESTREAM_GATE_BINARY")
	if !filepath.IsAbs(binary) {
		t.Fatal("checksum-verified scratch Litestream binary path required")
	}
	scratch := t.TempDir()
	// #nosec G302 -- Private scratch directory needs owner traversal permission.
	if err := os.Chmod(scratch, 0700); err != nil {
		t.Fatal("private drill workspace unavailable")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	cli := gateCLI{binary: binary}
	result, err := cli.Execute(ctx, Command{Args: []string{"version"}, Credentials: credentials, Directory: scratch})
	if err != nil || strings.TrimSpace(string(result.Stdout)) != LitestreamVersion {
		t.Fatal("drill tool version mismatch")
	}
	remote := gateS3{destination: destination, credentials: credentials, prefix: prefix + "/"}
	initial, err := remote.list(ctx)
	if err != nil || len(initial) != 0 {
		t.Fatal("new drill prefix is not provably empty")
	}
	defer func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		keys, err := remote.list(cleanupCtx)
		if err != nil {
			t.Error("drill cleanup listing failed")
			return
		}
		sort.Strings(keys)
		listed, _ := json.Marshal(keys)
		fmt.Printf("listed_objects=%s\n", listed)
		for _, key := range keys {
			if _, err := remote.request(cleanupCtx, http.MethodDelete, key, nil, nil); err != nil {
				t.Error("drill cleanup delete failed")
				continue
			}
			data, _ := json.Marshal(key)
			fmt.Printf("deleted_object=%s\n", data)
		}
		remaining, err := remote.list(cleanupCtx)
		if err != nil || len(remaining) != 0 {
			t.Error("drill cleanup not proven empty")
		}
	}()
	dataDirectory := filepath.Join(scratch, "data")
	restoreRoot := filepath.Join(scratch, "restores")
	for _, dir := range []string{dataDirectory, restoreRoot} {
		if err := os.Mkdir(dir, 0700); err != nil {
			t.Fatal("drill workspace unavailable")
		}
	}
	database := filepath.Join(dataDirectory, "fixture.sqlite")
	db, err := sql.Open("sqlite", database)
	if err != nil {
		t.Fatal("fixture open failed")
	}
	defer func() { mustRestoreTest(t, db.Close()) }()
	ddl, err := os.ReadFile("testdata/fixture.sql")
	if err != nil {
		t.Fatal("fixture schema unavailable")
	}
	if _, err := db.ExecContext(ctx, string(ddl)); err != nil {
		t.Fatal("fixture initialization failed")
	}
	catalog := [][4]any{{"table", "fixture_commits", "fixture_commits", "CREATE TABLE fixture_commits(sequence INTEGER PRIMARY KEY, marker TEXT NOT NULL, committed_at TEXT NOT NULL)"}}
	encoded, _ := json.Marshal(catalog)
	digest := sha256.Sum256(encoded)
	schema := SchemaObservation{State: VerifiedSchema, Marker: "fixture-v1", CatalogSHA256: hex.EncodeToString(digest[:])}
	if _, err := db.ExecContext(ctx, `INSERT INTO brine_schema_marker VALUES(1,?,?);`, schema.Marker, schema.CatalogSHA256); err != nil {
		t.Fatal("fixture commit failed")
	}
	if _, err := db.ExecContext(ctx, "PRAGMA journal_mode=WAL"); err != nil {
		t.Fatal("fixture WAL unavailable")
	}
	if err := os.Chmod(database, 0600); err != nil {
		t.Fatal("fixture mode failed")
	}
	ltxDestination := destination
	ltxDestination.Prefix = prefix + "/ltx"
	socket := filepath.Join(scratch, "control.sock")
	config := append(renderConfig(database, ltxDestination), []byte("socket:\n  enabled: true\n  path: "+strconv.Quote(socket)+"\n")...)
	configPath := filepath.Join(scratch, "replicate.yml")
	if err := os.WriteFile(configPath, config, 0600); err != nil {
		t.Fatal("replica config unavailable")
	}
	// #nosec G204 G702 -- Operator-supplied absolute checksum-verified scratch binary; fixed separate replication argv, no shell.
	command := exec.CommandContext(ctx, binary, "replicate", "-config", configPath, "-no-expand-env")
	command.Dir = scratch
	command.Env = gateEnvironment(credentials, scratch)
	command.Stdout = &boundedBuffer{}
	command.Stderr = &boundedBuffer{}
	command.WaitDelay = 100 * time.Millisecond
	if err := command.Start(); err != nil {
		t.Fatal("replicator start failed")
	}
	defer func() { _ = command.Process.Kill(); _ = command.Wait() }()
	ready := time.NewTicker(100 * time.Millisecond)
	defer ready.Stop()
	for {
		if _, err := os.Stat(socket); err == nil {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal("replicator control socket unavailable")
		case <-ready.C:
		}
	}
	committed := time.Now().UTC().Truncate(time.Second)
	insert, err := db.ExecContext(ctx, `INSERT INTO fixture_commits VALUES(7,'marker-7',?);`, committed.Format(time.RFC3339))
	if err != nil {
		t.Fatal("sentinel commit failed")
	}
	affected, err := insert.RowsAffected()
	if err != nil || affected != 1 {
		t.Fatal("sentinel commit result invalid")
	}
	var journalMode string
	var sourceCount int64
	if err := db.QueryRowContext(ctx, "PRAGMA journal_mode").Scan(&journalMode); err != nil {
		t.Fatal("writer journal mode unavailable")
	}
	if err := db.QueryRowContext(ctx, "SELECT count(*) FROM fixture_commits").Scan(&sourceCount); err != nil {
		t.Fatal("source sentinel count unavailable")
	}
	var walSize int64
	if info, err := os.Stat(database + "-wal"); err == nil {
		walSize = info.Size()
	}
	fmt.Printf("fixture_commit_diagnostic={\"rows_affected\":%d,\"journal_mode\":%q,\"wal_bytes\":%d,\"source_sentinel_rows\":%d}\n", affected, journalMode, walSize, sourceCount)
	synced, err := cli.Execute(ctx, Command{Args: []string{"sync", "-wait", "-json", "-timeout", "120", "-socket", socket, database}, Credentials: credentials, Directory: scratch})
	if err != nil {
		t.Fatal("remote upload barrier failed")
	}
	var syncReceipt struct {
		Path        string `json:"db_path"`
		TXID        uint64 `json:"txid"`
		ReplicaTXID uint64 `json:"replica_txid"`
	}
	if err := json.Unmarshal(synced.Stdout, &syncReceipt); err != nil || syncReceipt.Path != database || syncReceipt.TXID == 0 || syncReceipt.ReplicaTXID < syncReceipt.TXID {
		t.Fatal("invalid remote upload barrier")
	}
	binding := Binding{ID: "gate-binding", Epoch: "gate-epoch", CredentialRef: "gate-credentials", Destination: ltxDestination}
	engine := Engine{Root: restoreRoot, Bindings: BindingReaderFunc(func(context.Context, string, string) (Binding, error) { return binding, nil }), Credentials: CredentialReaderFunc(func(context.Context, string) (Credentials, error) { return credentials, nil }), Observer: gateSchemaObserver{}, CLI: cli}
	request := Request{OperationID: "ltx-restore", Source: RestoreSource{Kind: LitestreamLTX, LTX: &LTXSource{BindingID: binding.ID, Epoch: binding.Epoch, TXID: syncReceipt.TXID, Barrier: &BarrierReceipt{BindingID: binding.ID, Epoch: binding.Epoch, TXID: syncReceipt.TXID, ReplicaTXID: syncReceipt.ReplicaTXID, Succeeded: true, ObservedAt: time.Now().UTC()}}}, Budget: 2 * time.Minute, ExpectedSchema: schema, Sentinel: &Sentinel{Table: "fixture_commits", Sequence: 7, Marker: "marker-7", CommittedAt: committed}, Invariants: []Invariant{{Kind: RowCount, Table: "fixture_commits", Count: 1}}}
	restored, err := engine.Test(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	gateRestoredCount(restored.DatabasePath)
	printGateReceipt(t, restored)
	// Snapshot a fixture copy, not the DB that Litestream currently has open.
	snapshotPath := filepath.Join(scratch, "snapshot.sqlite")
	if _, err := db.ExecContext(ctx, `VACUUM INTO ?`, snapshotPath); err != nil {
		t.Fatal("fixture snapshot failed")
	}
	// #nosec G304 -- Snapshot path belongs to this drill's private scratch directory.
	snapshot, err := os.ReadFile(snapshotPath)
	if err != nil || len(snapshot) > 16<<20 {
		t.Fatal("fixture snapshot unavailable or oversized")
	}
	snapshotDigest := sha256.Sum256(snapshot)
	binding.Destination = destination
	key := prefix + "/restore-points/gate-point/snapshot.sqlite"
	if _, err := remote.request(ctx, http.MethodPut, key, snapshot, map[string]string{"If-None-Match": "*"}); err != nil {
		t.Fatal("immutable snapshot upload failed")
	}
	request.OperationID = "snapshot-restore"
	request.Source = RestoreSource{Kind: SQLiteSnapshot, Snapshot: &SnapshotSource{BindingID: binding.ID, Epoch: binding.Epoch, PointID: "gate-point", ObjectKey: key, SHA256: hex.EncodeToString(snapshotDigest[:]), Size: int64(len(snapshot))}}
	restored, err = engine.Test(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	printGateReceipt(t, restored)
	// An empty snapshot is also a real uploaded object, not a missing-LTX shortcut.
	emptyPath := filepath.Join(scratch, "empty.sqlite")
	empty, err := sql.Open("sqlite", emptyPath)
	if err != nil {
		t.Fatal("empty fixture failed")
	}
	_, err = empty.ExecContext(ctx, "PRAGMA user_version=0")
	_ = empty.Close()
	if err != nil {
		t.Fatal("empty fixture failed")
	}
	// #nosec G304 -- Empty fixture path belongs to this drill's private scratch directory.
	emptyBytes, err := os.ReadFile(emptyPath)
	if err != nil {
		t.Fatal("empty fixture failed")
	}
	emptyDigest := sha256.Sum256(emptyBytes)
	emptyKey := prefix + "/restore-points/empty-point/snapshot.sqlite"
	if _, err := remote.request(ctx, http.MethodPut, emptyKey, emptyBytes, map[string]string{"If-None-Match": "*"}); err != nil {
		t.Fatal("empty snapshot upload failed")
	}
	request.OperationID = "empty-restore"
	request.ExpectedSchema = SchemaObservation{State: VerifiedEmpty, Marker: "brine-empty-v1", CatalogSHA256: "4f53cda18c2baa0c0354bb5f9a3ecbe5ed12ab4d8e11ba873c2f11161202b945"}
	request.Sentinel = nil
	request.Invariants = nil
	request.Source = RestoreSource{Kind: SQLiteSnapshot, Snapshot: &SnapshotSource{BindingID: binding.ID, Epoch: binding.Epoch, PointID: "empty-point", ObjectKey: emptyKey, SHA256: hex.EncodeToString(emptyDigest[:]), Size: int64(len(emptyBytes))}}
	restored, err = engine.Test(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	printGateReceipt(t, restored)
}

func printGateReceipt(t *testing.T, r Receipt) {
	t.Helper()
	data, err := json.Marshal(r)
	if err != nil {
		t.Fatal("receipt encoding failed")
	}
	fmt.Printf("restore_receipt=%s\n", data)
}

type gateCLI struct{ binary string }

func (g gateCLI) Execute(ctx context.Context, c Command) (CommandResult, error) {
	// #nosec G204 G702 -- Test-only operator-verified scratch binary and typed restore argv, never a shell.
	command := exec.CommandContext(ctx, g.binary, c.Args...)
	command.Dir = c.Directory
	command.Env = gateEnvironment(c.Credentials, c.Directory)
	output := &boundedBuffer{}
	command.Stdout = output
	command.Stderr = &boundedBuffer{}
	command.WaitDelay = 100 * time.Millisecond
	if err := command.Run(); err != nil {
		return CommandResult{}, refuse("gate_cli_failed")
	}
	data, truncated := output.snapshot()
	if len(c.Args) > 0 && c.Args[0] == "restore" {
		dry := false
		output := ""
		for i, arg := range c.Args {
			if arg == "-dry-run" {
				dry = true
			}
			if arg == "-o" && i+1 < len(c.Args) {
				output = c.Args[i+1]
			}
		}
		if !dry && output != "" {
			gateRestoredCount(output)
		}
	}
	if len(c.Args) > 0 && (c.Args[0] == "sync" || c.Args[0] == "restore") {
		safe := make(map[string]any)
		var decoded map[string]any
		if err := json.Unmarshal(data, &decoded); err == nil {
			for _, key := range []string{"txid", "replica_txid", "replica", "integrity_check", "min_txid", "max_txid"} {
				if value, ok := decoded[key]; ok {
					safe[key] = value
				}
			}
		} else {
			safe["stdout_json_valid"] = false
			safe["stdout_bytes"] = len(data)
		}
		safe["command"] = c.Args[0]
		for i, arg := range c.Args {
			if arg == "-txid" && i+1 < len(c.Args) {
				safe["requested_txid"] = c.Args[i+1]
			}
			if arg == "-dry-run" {
				safe["dry_run"] = true
			}
		}
		encoded, _ := json.Marshal(safe)
		fmt.Printf("cli_position_diagnostic=%s\n", encoded)
	}
	return CommandResult{Stdout: data, Truncated: truncated}, nil
}
func gateEnvironment(c Credentials, directory string) []string {
	environment := []string{"PATH=/usr/bin:/bin", "HOME=" + directory, "LC_ALL=C", "TMPDIR=" + directory, "AWS_EC2_METADATA_DISABLED=true", "AWS_ACCESS_KEY_ID=" + c.AccessKey, "AWS_SECRET_ACCESS_KEY=" + c.SecretKey}
	if c.SessionToken != "" {
		environment = append(environment, "AWS_SESSION_TOKEN="+c.SessionToken)
	}
	return environment
}

// Only the gate fixture's exact DDL is accepted. Production uses P04-01's observer.
type gateSchemaObserver struct{}

func (gateSchemaObserver) Observe(ctx context.Context, tx *sql.Tx) (SchemaObservation, error) {
	rows, err := tx.QueryContext(ctx, `SELECT type,name,tbl_name,sql FROM sqlite_schema WHERE name NOT GLOB 'sqlite_*' ORDER BY type COLLATE BINARY, name COLLATE BINARY`)
	if err != nil {
		return SchemaObservation{}, refuse("gate_catalog")
	}
	catalog := make([][4]any, 0)
	markerPresent := false
	for rows.Next() {
		var kind, name, table string
		var ddl sql.NullString
		if err := rows.Scan(&kind, &name, &table, &ddl); err != nil {
			_ = rows.Close() // Preserve the catalog verification failure.
			return SchemaObservation{}, refuse("gate_catalog")
		}
		switch name {
		case "brine_schema_marker":
			if kind != "table" || table != name || ddl.String != "CREATE TABLE brine_schema_marker(id INTEGER PRIMARY KEY CHECK(id=1), marker TEXT NOT NULL, catalog_sha256 TEXT NOT NULL)" {
				_ = rows.Close() // Preserve the schema verification failure.
				return SchemaObservation{}, refuse("gate_marker_structure")
			}
			markerPresent = true
		case "_litestream_seq":
			if kind != "table" || table != name || ddl.String != "CREATE TABLE _litestream_seq (id INTEGER PRIMARY KEY, seq INTEGER)" {
				_ = rows.Close() // Preserve the schema verification failure.
				return SchemaObservation{}, refuse("gate_internal_structure")
			}
		case "_litestream_lock":
			if kind != "table" || table != name || ddl.String != "CREATE TABLE _litestream_lock (id INTEGER)" {
				_ = rows.Close() // Preserve the schema verification failure.
				return SchemaObservation{}, refuse("gate_internal_structure")
			}
		default:
			var value any
			if ddl.Valid {
				value = ddl.String
			}
			catalog = append(catalog, [4]any{kind, name, table, value})
		}
	}
	err = rows.Err()
	closeErr := rows.Close()
	if err != nil || closeErr != nil {
		return SchemaObservation{}, refuse("gate_catalog")
	}
	data, _ := json.Marshal(catalog)
	digest := sha256.Sum256(data)
	if !markerPresent && len(catalog) == 0 {
		return SchemaObservation{State: VerifiedEmpty, Marker: "brine-empty-v1", CatalogSHA256: hex.EncodeToString(digest[:])}, nil
	}
	var count, id int64
	var marker, fingerprint string
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM brine_schema_marker`).Scan(&count); err != nil || count != 1 {
		return SchemaObservation{}, refuse("gate_marker")
	}
	if err := tx.QueryRowContext(ctx, `SELECT id, marker, catalog_sha256 FROM brine_schema_marker`).Scan(&id, &marker, &fingerprint); err != nil || id != 1 || fingerprint != hex.EncodeToString(digest[:]) {
		return SchemaObservation{}, refuse("gate_marker")
	}
	return SchemaObservation{State: VerifiedSchema, Marker: marker, CatalogSHA256: fingerprint}, nil
}

type gateS3 struct {
	destination Destination
	credentials Credentials
	prefix      string
}

func (g gateS3) request(ctx context.Context, method, key string, body []byte, headers map[string]string) ([]byte, error) {
	if key != "" && (!strings.HasPrefix(key, g.prefix) || !validKey(key)) {
		return nil, refuse("gate_object_scope")
	}
	endpoint, _ := url.Parse(g.destination.Endpoint)
	endpoint.Path = "/" + g.destination.Bucket + "/" + key
	request, err := http.NewRequestWithContext(ctx, method, endpoint.String(), bytes.NewReader(body))
	if err != nil {
		return nil, refuse("gate_request")
	}
	for name, value := range headers {
		request.Header.Set(name, value)
	}
	if key == "" {
		query := request.URL.Query()
		query.Set("list-type", "2")
		query.Set("prefix", g.prefix)
		request.URL.RawQuery = query.Encode()
	}
	gateSign(request, g.destination.Region, g.credentials, body)
	client := &http.Client{Timeout: 30 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	response, err := client.Do(request)
	if err != nil {
		return nil, refuse("gate_http")
	}
	defer func() { _ = response.Body.Close() }() // HTTP read errors and bounds are checked below.
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, refuse("gate_http_status")
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, (1<<20)+1))
	if err != nil || len(data) > 1<<20 {
		return nil, refuse("gate_response_bound")
	}
	return data, nil
}
func (g gateS3) list(ctx context.Context) ([]string, error) {
	data, err := g.request(ctx, http.MethodGet, "", nil, nil)
	if err != nil {
		return nil, err
	}
	var result struct {
		Truncated bool `xml:"IsTruncated"`
		Contents  []struct {
			Key string `xml:"Key"`
		} `xml:"Contents"`
	}
	if err := xml.Unmarshal(data, &result); err != nil || result.Truncated {
		return nil, refuse("gate_listing_incomplete")
	}
	keys := make([]string, 0, len(result.Contents))
	for _, object := range result.Contents {
		if !strings.HasPrefix(object.Key, g.prefix) || !validKey(object.Key) {
			return nil, refuse("gate_listing_scope")
		}
		keys = append(keys, object.Key)
	}
	return keys, nil
}
func gateSign(request *http.Request, region string, c Credentials, body []byte) {
	now := time.Now().UTC()
	timestamp := now.Format("20060102T150405Z")
	date := now.Format("20060102")
	payload := sha256.Sum256(body)
	payloadHash := hex.EncodeToString(payload[:])
	request.Header.Set("X-Amz-Date", timestamp)
	request.Header.Set("X-Amz-Content-Sha256", payloadHash)
	if c.SessionToken != "" {
		request.Header.Set("X-Amz-Security-Token", c.SessionToken)
	}
	names := []string{"host"}
	values := map[string]string{"host": request.URL.Host}
	for name, list := range request.Header {
		name = strings.ToLower(name)
		names = append(names, name)
		values[name] = strings.Join(list, ",")
	}
	sort.Strings(names)
	canonicalHeaders := ""
	for _, name := range names {
		canonicalHeaders += name + ":" + values[name] + "\n"
	}
	signedHeaders := strings.Join(names, ";")
	canonical := request.Method + "\n" + request.URL.EscapedPath() + "\n" + request.URL.RawQuery + "\n" + canonicalHeaders + "\n" + signedHeaders + "\n" + payloadHash
	scope := date + "/" + region + "/s3/aws4_request"
	digest := sha256.Sum256([]byte(canonical))
	toSign := "AWS4-HMAC-SHA256\n" + timestamp + "\n" + scope + "\n" + hex.EncodeToString(digest[:])
	key := hmacSHA([]byte("AWS4"+c.SecretKey), date)
	key = hmacSHA(key, region)
	key = hmacSHA(key, "s3")
	key = hmacSHA(key, "aws4_request")
	request.Header.Set("Authorization", "AWS4-HMAC-SHA256 Credential="+c.AccessKey+"/"+scope+", SignedHeaders="+signedHeaders+", Signature="+hex.EncodeToString(hmacSHA(key, toSign)))
}

func gateRestoredCount(path string) {
	u := url.URL{Scheme: "file", Path: path}
	query := u.Query()
	query.Set("mode", "ro")
	query.Set("immutable", "1")
	u.RawQuery = query.Encode()
	db, err := sql.Open("sqlite", u.String())
	if err != nil {
		fmt.Println("restored_sentinel_rows=unknown")
		return
	}
	defer func() { _ = db.Close() }() // Diagnostic-only read; no receipt or success decision depends on it.
	var count int64
	if err := db.QueryRow(`SELECT count(*) FROM fixture_commits`).Scan(&count); err != nil {
		fmt.Println("restored_sentinel_rows=unknown")
		return
	}
	fmt.Printf("restored_sentinel_rows=%d\n", count)
}
