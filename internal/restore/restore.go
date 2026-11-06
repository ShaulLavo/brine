package restore

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

type SQLiteEngine struct {
	// Root is a dedicated runner-owned private restore workspace, never a data root.
	Root             string
	Bindings         BindingReader
	Credentials      CredentialReader
	Observer         SQLiteSchemaObserver
	CLI              CLI
	MaxSnapshotBytes int64
	// AllowHTTPForTests admits a local fake only. Production requires HTTPS.
	AllowHTTPForTests bool
}

// Test retains verified output for a caller that consumes the database file.
func (e *SQLiteEngine) Test(ctx context.Context, r SQLiteRequest) (SQLiteReceipt, error) {
	return e.test(ctx, r, false)
}

// TestMetadata owns the disposable output until verification ends. It returns
// only metadata, after disposal succeeds, even when the context was canceled.
func (e *SQLiteEngine) TestMetadata(ctx context.Context, r SQLiteRequest) (SQLiteReceipt, error) {
	return e.test(ctx, r, true)
}

func (e *SQLiteEngine) test(ctx context.Context, r SQLiteRequest, metadataOnly bool) (receipt SQLiteReceipt, resultErr error) {
	if source := r.Source.Snapshot; source != nil {
		copy := *source
		r.Source.Snapshot = &copy
	}
	if source := r.Source.LTX; source != nil {
		copy := *source
		if barrier := source.Barrier; barrier != nil {
			barrierCopy := *barrier
			copy.Barrier = &barrierCopy
		}
		r.Source.LTX = &copy
	}
	id, epoch, err := requestReference(r)
	if err != nil {
		return SQLiteReceipt{}, err
	}
	if !token.MatchString(id) || !token.MatchString(epoch) || !token.MatchString(r.OperationID) || r.Budget <= 0 || r.Budget > 30*time.Minute || e.Bindings == nil || e.Credentials == nil || e.Observer == nil {
		return SQLiteReceipt{}, refuse("invalid_request")
	}
	if err := validateChecks(r); err != nil {
		return SQLiteReceipt{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, r.Budget)
	defer cancel()
	binding, err := e.Bindings.ReadBinding(ctx, id, epoch)
	if err != nil {
		return SQLiteReceipt{}, refuse("binding_unavailable")
	}
	if binding.ID != id || binding.Epoch != epoch || !token.MatchString(binding.CredentialRef) {
		return SQLiteReceipt{}, refuse("binding_mismatch")
	}
	if err := validateDestination(binding.Destination, e.AllowHTTPForTests); err != nil {
		return SQLiteReceipt{}, err
	}
	if s := r.Source.Snapshot; s != nil {
		limit := e.MaxSnapshotBytes
		if limit == 0 {
			limit = 1 << 30
		}
		expected := binding.Destination.Prefix + "/restore-points/" + s.PointID + "/snapshot.sqlite"
		if limit <= 0 || s.Size > limit || s.ObjectKey != expected {
			return SQLiteReceipt{}, refuse("snapshot_scope_or_size")
		}
	}
	now := time.Now().UTC()
	if b := r.Source.LTX; b != nil && b.Barrier != nil && b.Barrier.ObservedAt.After(now) {
		return SQLiteReceipt{}, refuse("invalid_barrier")
	}
	if r.Coverage != nil && (!samePoint(r.Coverage.Source, r.Source) || r.Coverage.CoveredThrough.IsZero() || !r.Coverage.CoveredThrough.Before(now)) {
		return SQLiteReceipt{}, refuse("invalid_coverage")
	}
	credentials, err := e.Credentials.ReadCredentials(ctx, binding.CredentialRef)
	if err != nil {
		return SQLiteReceipt{}, refuse("credentials_unavailable")
	}
	// Readers may issue fresh scoped credentials while this call is running.
	// Compare their timestamps at the credential boundary, not before the read.
	if err := validateCredentials(credentials, time.Now().UTC(), r.Budget); err != nil {
		return SQLiteReceipt{}, err
	}
	workspace, err := newSQLiteWorkspace(e.Root, r.OperationID)
	if err != nil {
		return SQLiteReceipt{}, err
	}
	success := false
	defer func() {
		if !success || metadataOnly {
			resultErr = errors.Join(resultErr, workspace.dispose())
		}
		resultErr = errors.Join(resultErr, workspace.close())
		if resultErr != nil {
			receipt = SQLiteReceipt{}
		}
	}()
	directory := filepath.Join(e.Root, r.OperationID)
	output := filepath.Join(directory, "restored.sqlite")
	toolVersion := SnapshotToolVersion
	var recovered uint64
	if r.Source.Kind == SQLiteSnapshot {
		err = downloadSnapshot(ctx, binding.Destination, credentials, *r.Source.Snapshot, output, e.AllowHTTPForTests)
	} else {
		toolVersion = LitestreamVersion
		recovered, err = e.restoreLTX(ctx, binding, credentials, *r.Source.LTX, directory, output)
	}
	if err != nil {
		return SQLiteReceipt{}, err
	}
	if r.Latest {
		r.Source.LTX.TXID = recovered
	}
	if err := validateOutput(output); err != nil {
		return SQLiteReceipt{}, err
	}
	schema, sentinel, err := verify(ctx, output, e.Observer, r)
	if err != nil {
		return SQLiteReceipt{}, err
	}
	if ctx.Err() != nil {
		return SQLiteReceipt{}, refuse("deadline")
	}
	observed := time.Now().UTC()
	receipt = SQLiteReceipt{OperationID: r.OperationID, Source: r.Source, ToolVersion: toolVersion, RecoveredTXID: recovered, ObservedAt: observed, Schema: schema, Sentinel: sentinel, DatabasePath: output, LossWindow: LossWindow{State: LossUnknown, Reason: "No independent last-commit coverage proof; asynchronous replication may lose recent writes."}}
	receipt.IntegrityCheck = "passed"
	receipt.ForeignKeyCheck = "passed"
	receipt.InvariantCheck = "unknown"
	if sentinel != nil || len(r.Invariants) > 0 || schema.State == VerifiedEmpty {
		receipt.InvariantCheck = "passed"
	}
	if s := r.Source.LTX; s != nil {
		receipt.PositionEvidence = "pinned_cli_exact_plan_and_successful_restore"
		if s.Recoverability {
			receipt.PositionEvidence = "remote_dry_run_and_restore"
		}
		receipt.RequestedTXID = s.TXID
		receipt.Barrier = s.Barrier
	}
	if r.Coverage != nil {
		receipt.Coverage = &CoverageEvidence{Source: receipt.Source, CoveredThrough: r.Coverage.CoveredThrough.UTC()}
		receipt.LossWindow = LossWindow{State: LossBounded, From: r.Coverage.CoveredThrough.UTC(), To: observed, Reason: "Independent source-bound coverage proof bounds possible loss; later writes may be absent."}
	}
	success = true
	if metadataOnly {
		receipt.DatabasePath = ""
	}
	return receipt, nil
}

func validateCredentials(c Credentials, now time.Time, budget time.Duration) error {
	for _, value := range []string{c.AccessKey, c.SecretKey, c.SessionToken} {
		if len(value) > 8192 {
			return refuse("invalid_credentials")
		}
		for _, char := range value {
			if char < '!' || char > '~' {
				return refuse("invalid_credentials")
			}
		}
	}
	if c.AccessKey == "" || c.SecretKey == "" || c.ReceivedAt.After(now) {
		return refuse("invalid_credentials")
	}
	if !c.ExpiresAt.IsZero() && !c.ExpiresAt.After(now.Add(budget)) {
		return refuse("credential_lifetime_insufficient")
	}
	return nil
}
func privateDirectory(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return refuse("workspace_not_private")
	}
	return privateDirectoryInfo(info)
}

func privateDirectoryInfo(info os.FileInfo) error {
	if !info.IsDir() || info.Mode().Perm() != 0700 {
		return refuse("workspace_not_private")
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || int64(stat.Uid) != int64(os.Getuid()) {
		return refuse("workspace_owner")
	}
	return nil
}

type sqliteWorkspace struct {
	parent        *os.Root
	directory     *os.Root
	root          string
	operation     string
	rootInfo      os.FileInfo
	directoryInfo os.FileInfo
}

func newDirectory(root, operation string) (string, error) {
	workspace, err := newSQLiteWorkspace(root, operation)
	if err != nil {
		return "", err
	}
	return filepath.Join(root, operation), workspace.close()
}

func newSQLiteWorkspace(root, operation string) (*sqliteWorkspace, error) {
	if !filepath.IsAbs(root) || filepath.Clean(root) != root || !token.MatchString(operation) {
		return nil, refuse("invalid_workspace")
	}
	resolved, err := filepath.EvalSymlinks(root)
	if err != nil || resolved != root {
		return nil, refuse("invalid_workspace")
	}
	if err := privateDirectory(root); err != nil {
		return nil, err
	}
	parent, err := os.OpenRoot(root)
	if err != nil {
		return nil, refuse("workspace_unavailable")
	}
	workspace := &sqliteWorkspace{parent: parent, root: root, operation: operation}
	if err := workspace.create(); err != nil {
		return nil, errors.Join(err, workspace.close())
	}
	return workspace, nil
}

func (w *sqliteWorkspace) create() error {
	var err error
	w.rootInfo, err = w.parent.Stat(".")
	if err != nil {
		return refuse("workspace_unavailable")
	}
	if err := privateDirectoryInfo(w.rootInfo); err != nil {
		return err
	}
	if err := w.parent.Mkdir(w.operation, 0700); err != nil {
		return refuse("operation_directory_exists_or_unavailable")
	}
	w.directoryInfo, err = w.parent.Lstat(w.operation)
	if err != nil {
		return refuse("workspace_not_private")
	}
	if err := privateDirectoryInfo(w.directoryInfo); err != nil {
		return err
	}
	w.directory, err = w.parent.OpenRoot(w.operation)
	if err != nil {
		return refuse("workspace_unavailable")
	}
	held, err := w.directory.Stat(".")
	if err != nil || !os.SameFile(held, w.directoryInfo) {
		return refuse("workspace_changed")
	}
	return nil
}

func (w *sqliteWorkspace) close() error {
	var closeErr error
	if w.directory != nil {
		closeErr = w.directory.Close()
	}
	if err := errors.Join(closeErr, w.parent.Close()); err != nil {
		return refuse("workspace_close")
	}
	return nil
}

func (w *sqliteWorkspace) dispose() error {
	// Neither the receipt nor a caller-supplied output path grants deletion.
	// Keep the held parent and original directory identities through disposal.
	resolved, err := filepath.EvalSymlinks(w.root)
	if err != nil || resolved != w.root {
		return refuse("workspace_changed")
	}
	parent, err := os.Lstat(w.root)
	if err != nil || !os.SameFile(parent, w.rootInfo) {
		return refuse("workspace_changed")
	}
	current, err := w.parent.Lstat(w.operation)
	if err != nil || !current.IsDir() || !os.SameFile(current, w.directoryInfo) {
		return refuse("workspace_changed")
	}
	if err := w.removeContents(); err != nil {
		return err
	}
	current, err = w.parent.Lstat(w.operation)
	if err != nil || !os.SameFile(current, w.directoryInfo) {
		return refuse("workspace_changed")
	}
	// Only remove the now-empty directory by name. Recursive deletion stays
	// anchored in the held operation directory even if its name changes.
	if err := w.parent.Remove(w.operation); err != nil {
		return refuse("workspace_cleanup")
	}
	return nil
}

func (w *sqliteWorkspace) removeContents() error {
	directory, err := w.directory.Open(".")
	if err != nil {
		return refuse("workspace_cleanup")
	}
	entries, readErr := directory.ReadDir(-1)
	if err := errors.Join(readErr, directory.Close()); err != nil {
		return refuse("workspace_cleanup")
	}
	for _, entry := range entries {
		if err := w.directory.RemoveAll(entry.Name()); err != nil {
			return refuse("workspace_cleanup")
		}
	}
	return nil
}

func validateOutput(path string) error {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() == 0 {
		return refuse("invalid_output")
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || int64(stat.Uid) != int64(os.Getuid()) || stat.Nlink != 1 {
		return refuse("invalid_output_owner")
	}
	for _, suffix := range []string{"-wal", "-shm", "-journal", "-litestream"} {
		if _, err := os.Lstat(path + suffix); !os.IsNotExist(err) {
			return refuse("unexpected_output_sibling")
		}
	}
	if err := os.Chmod(path, 0600); err != nil {
		return refuse("output_mode")
	}
	return nil
}
func (e *SQLiteEngine) restoreLTX(ctx context.Context, b Binding, c Credentials, s LTXSource, directory, output string) (recovered uint64, resultErr error) {
	cli := e.CLI
	if cli == nil {
		cli = ExecCLI{}
	}
	command, err := ltxRestoreCommand(ctx, cli, b, c, s.TXID, directory, output)
	if err != nil {
		return 0, err
	}
	defer func() {
		if err := os.Remove(filepath.Join(directory, "restore.yml")); err != nil {
			recovered, resultErr = 0, errors.Join(resultErr, refuse("restore_config_cleanup"))
		}
	}()
	selected, err := planLTX(ctx, cli, command, output, s.TXID)
	if err != nil {
		return 0, err
	}
	if s.TXID == 0 {
		s.TXID = selected
		command.Args = append(command.Args, "-txid", txidString(selected))
	}
	// v0.5.17's restore JSON echoes the requested TXID. The independently selected
	// plan boundary must match first; an arbitrary mid-file TXID is not evidence.
	command.Args = append(command.Args, filepath.Join(directory, "remote-selector.sqlite"))
	result, err := cli.Execute(ctx, command)
	if err != nil || result.Truncated {
		return 0, refuse("ltx_restore_failed")
	}
	var restored struct {
		Path      string `json:"db_path"`
		Replica   string `json:"replica"`
		TXID      string `json:"txid"`
		Integrity string `json:"integrity_check"`
	}
	if err := json.Unmarshal(result.Stdout, &restored); err != nil || restored.Path != output || restored.Replica != "s3" || restored.TXID != txidString(s.TXID) || restored.Integrity != "full" {
		return 0, refuse("ltx_receipt_mismatch")
	}
	return s.TXID, nil
}

func renderConfig(selector string, d Destination) []byte {
	quote := func(value string) string { data, _ := json.Marshal(value); return string(data) }
	config := "logging:\n  stderr: true\ndbs:\n  - path: " + quote(selector) + "\n    replica:\n      type: s3\n      bucket: " + quote(d.Bucket) + "\n      path: " + quote(d.Prefix) + "\n      endpoint: " + quote(d.Endpoint) + "\n      region: " + quote(d.Region) + "\n      force-path-style: "
	if d.PathStyle {
		config += "true\n"
	} else {
		config += "false\n"
	}
	return []byte(config)
}

func requestReference(r SQLiteRequest) (string, string, error) {
	if !r.Latest {
		return r.Source.reference()
	}
	s := r.Source.LTX
	if r.Source.Kind != LitestreamLTX || r.Source.Snapshot != nil || s == nil || s.TXID != 0 || !s.Recoverability || s.Barrier != nil || r.Coverage != nil {
		return "", "", refuse("invalid_latest_source")
	}
	return s.BindingID, s.Epoch, nil
}
