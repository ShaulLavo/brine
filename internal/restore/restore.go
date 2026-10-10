package restore

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

type Engine struct {
	// Root is a dedicated runner-owned private restore workspace, never a data root.
	Root             string
	Bindings         BindingReader
	Credentials      CredentialReader
	Observer         SchemaObserver
	CLI              CLI
	MaxSnapshotBytes int64
	// AllowHTTPForTests admits a local fake only. Production requires HTTPS.
	AllowHTTPForTests bool
}

func (e *Engine) Test(ctx context.Context, r Request) (Receipt, error) {
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
	id, epoch, err := r.Source.reference()
	if err != nil {
		return Receipt{}, err
	}
	if !token.MatchString(id) || !token.MatchString(epoch) || !token.MatchString(r.OperationID) || r.Budget <= 0 || r.Budget > 30*time.Minute || e.Bindings == nil || e.Credentials == nil || e.Observer == nil {
		return Receipt{}, refuse("invalid_request")
	}
	if err := validateChecks(r); err != nil {
		return Receipt{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, r.Budget)
	defer cancel()
	binding, err := e.Bindings.ReadBinding(ctx, id, epoch)
	if err != nil {
		return Receipt{}, refuse("binding_unavailable")
	}
	if binding.ID != id || binding.Epoch != epoch || !token.MatchString(binding.CredentialRef) {
		return Receipt{}, refuse("binding_mismatch")
	}
	if err := validateDestination(binding.Destination, e.AllowHTTPForTests); err != nil {
		return Receipt{}, err
	}
	if s := r.Source.Snapshot; s != nil {
		limit := e.MaxSnapshotBytes
		if limit == 0 {
			limit = 1 << 30
		}
		expected := binding.Destination.Prefix + "/restore-points/" + s.PointID + "/snapshot.sqlite"
		if limit <= 0 || s.Size > limit || s.ObjectKey != expected {
			return Receipt{}, refuse("snapshot_scope_or_size")
		}
	}
	now := time.Now().UTC()
	if b := r.Source.LTX; b != nil && b.Barrier != nil && b.Barrier.ObservedAt.After(now) {
		return Receipt{}, refuse("invalid_barrier")
	}
	if r.Coverage != nil && (!samePoint(r.Coverage.Source, r.Source) || r.Coverage.CoveredThrough.IsZero() || !r.Coverage.CoveredThrough.Before(now)) {
		return Receipt{}, refuse("invalid_coverage")
	}
	credentials, err := e.Credentials.ReadCredentials(ctx, binding.CredentialRef)
	if err != nil {
		return Receipt{}, refuse("credentials_unavailable")
	}
	// Readers may issue fresh scoped credentials while this call is running.
	// Compare their timestamps at the credential boundary, not before the read.
	if err := validateCredentials(credentials, time.Now().UTC(), r.Budget); err != nil {
		return Receipt{}, err
	}
	directory, err := newDirectory(e.Root, r.OperationID)
	if err != nil {
		return Receipt{}, err
	}
	success := false
	defer func() {
		if !success {
			_ = os.RemoveAll(directory)
		}
	}()
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
		return Receipt{}, err
	}
	if err := validateOutput(output); err != nil {
		return Receipt{}, err
	}
	schema, sentinel, err := verify(ctx, output, e.Observer, r)
	if err != nil {
		return Receipt{}, err
	}
	if ctx.Err() != nil {
		return Receipt{}, refuse("deadline")
	}
	observed := time.Now().UTC()
	receipt := Receipt{OperationID: r.OperationID, Source: r.Source, ToolVersion: toolVersion, RecoveredTXID: recovered, ObservedAt: observed, Schema: schema, Sentinel: sentinel, DatabasePath: output, LossWindow: LossWindow{State: LossUnknown, Reason: "No independent last-commit coverage proof; asynchronous replication may lose recent writes."}}
	receipt.IntegrityCheck = "passed"
	receipt.ForeignKeyCheck = "passed"
	receipt.InvariantCheck = "unknown"
	if sentinel != nil || len(r.Invariants) > 0 || schema.State == VerifiedEmpty {
		receipt.InvariantCheck = "passed"
	}
	if s := r.Source.LTX; s != nil {
		receipt.PositionEvidence = "pinned_cli_exact_plan_and_successful_restore"
		receipt.RequestedTXID = s.TXID
		receipt.Barrier = s.Barrier
	}
	if r.Coverage != nil {
		receipt.Coverage = &CoverageEvidence{Source: receipt.Source, CoveredThrough: r.Coverage.CoveredThrough.UTC()}
		receipt.LossWindow = LossWindow{State: LossBounded, From: r.Coverage.CoveredThrough.UTC(), To: observed, Reason: "Independent source-bound coverage proof bounds possible loss; later writes may be absent."}
	}
	success = true
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
	if err != nil || !info.IsDir() || info.Mode().Perm() != 0700 {
		return refuse("workspace_not_private")
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || int64(stat.Uid) != int64(os.Getuid()) {
		return refuse("workspace_owner")
	}
	return nil
}
func newDirectory(root, operation string) (string, error) {
	if !filepath.IsAbs(root) || filepath.Clean(root) != root {
		return "", refuse("invalid_workspace")
	}
	resolved, err := filepath.EvalSymlinks(root)
	if err != nil || resolved != root {
		return "", refuse("invalid_workspace")
	}
	if err := privateDirectory(root); err != nil {
		return "", err
	}
	handle, err := os.OpenRoot(root)
	if err != nil {
		return "", refuse("workspace_unavailable")
	}
	defer func() { _ = handle.Close() }() // Read-only root handle; close cannot change the created directory.
	if err := handle.Mkdir(operation, 0700); err != nil {
		return "", refuse("operation_directory_exists_or_unavailable")
	}
	directory := filepath.Join(root, operation)
	if err := privateDirectory(directory); err != nil {
		return "", err
	}
	return directory, nil
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
func (e *Engine) restoreLTX(ctx context.Context, b Binding, c Credentials, s LTXSource, directory, output string) (recovered uint64, resultErr error) {
	cli := e.CLI
	if cli == nil {
		cli = ExecCLI{}
	}
	version, err := cli.Execute(ctx, Command{Args: []string{"version"}, Credentials: c, Directory: directory})
	if err != nil || version.Truncated || strings.TrimSpace(string(version.Stdout)) != LitestreamVersion {
		return 0, refuse("litestream_version")
	}
	selector := filepath.Join(directory, "remote-selector.sqlite")
	configPath := filepath.Join(directory, "restore.yml")
	config := renderConfig(selector, b.Destination)
	if err := os.WriteFile(configPath, config, 0600); err != nil {
		return 0, refuse("restore_config")
	}
	defer func() {
		if err := os.Remove(configPath); err != nil && resultErr == nil {
			recovered, resultErr = 0, refuse("restore_config_cleanup")
		}
	}()
	base := []string{"restore", "-config", configPath, "-no-expand-env", "-o", output, "-txid", txidString(s.TXID), "-json", "-integrity-check", "full"}
	planResult, err := cli.Execute(ctx, Command{Args: append(append([]string(nil), base...), "-dry-run", selector), Credentials: c, Directory: directory})
	if err != nil || planResult.Truncated {
		return 0, refuse("ltx_plan_failed")
	}
	var plan struct {
		Target  string `json:"target_path"`
		Replica string `json:"replica"`
		MaxTXID string `json:"max_txid"`
	}
	if err := json.Unmarshal(planResult.Stdout, &plan); err != nil || plan.Target != output || plan.Replica != "s3" || plan.MaxTXID != txidString(s.TXID) {
		return 0, refuse("ltx_point_not_exact")
	}
	// v0.5.17's restore JSON echoes the requested TXID. The independently selected
	// plan boundary must match first; an arbitrary mid-file TXID is not evidence.
	result, err := cli.Execute(ctx, Command{Args: append(base, selector), Credentials: c, Directory: directory})
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
