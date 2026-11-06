package restore

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// RemoteWatermark returns the exact remote plan boundary selected by pinned
// Litestream, not a verified restore or local replication position. It never
// restores a database, and disposes its private workspace before returning.
// root must already be a dedicated private runner-owned workspace parent.
func RemoteWatermark(ctx context.Context, root string, cli CLI, b Binding, c Credentials) (txid uint64, resultErr error) {
	const budget = 30 * time.Second
	if !token.MatchString(b.ID) || !token.MatchString(b.Epoch) || !token.MatchString(b.CredentialRef) {
		return 0, refuse("binding_mismatch")
	}
	if err := validateDestination(b.Destination, false); err != nil {
		return 0, err
	}
	if err := validateCredentials(c, time.Now().UTC(), budget); err != nil {
		return 0, err
	}
	ctx, cancel := context.WithTimeout(ctx, budget)
	defer cancel()
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return 0, refuse("workspace_identity")
	}
	operation := "status-" + hex.EncodeToString(nonce[:])
	workspace, err := newSQLiteWorkspace(root, operation)
	if err != nil {
		return 0, err
	}
	defer func() {
		resultErr = errors.Join(resultErr, ctx.Err(), workspace.dispose(), workspace.close())
		if resultErr != nil {
			txid = 0
		}
	}()
	if cli == nil {
		cli = ExecCLI{}
	}
	directory := filepath.Join(root, operation)
	output := filepath.Join(directory, "restored.sqlite")
	command, err := ltxRestoreCommand(ctx, cli, b, c, 0, directory, output)
	if err != nil {
		return 0, err
	}
	return planLTX(ctx, cli, command, output, 0)
}

func ltxRestoreCommand(ctx context.Context, cli CLI, b Binding, c Credentials, txid uint64, directory, output string) (Command, error) {
	version, err := cli.Execute(ctx, Command{Args: []string{"version"}, Credentials: c, Directory: directory})
	if err != nil || version.Truncated || strings.TrimSpace(string(version.Stdout)) != LitestreamVersion {
		return Command{}, refuse("litestream_version")
	}
	selector := filepath.Join(directory, "remote-selector.sqlite")
	configPath := filepath.Join(directory, "restore.yml")
	if err := os.WriteFile(configPath, renderConfig(selector, b.Destination), 0600); err != nil {
		return Command{}, refuse("restore_config")
	}
	args := []string{"restore", "-config", configPath, "-no-expand-env", "-o", output, "-json", "-integrity-check", "full"}
	if txid != 0 {
		args = append(args, "-txid", txidString(txid))
	}
	return Command{Args: args, Credentials: c, Directory: directory}, nil
}

func planLTX(ctx context.Context, cli CLI, command Command, output string, requested uint64) (uint64, error) {
	command.Args = append(append([]string(nil), command.Args...), "-dry-run", filepath.Join(command.Directory, "remote-selector.sqlite"))
	result, err := cli.Execute(ctx, command)
	if err != nil || result.Truncated {
		return 0, refuse("ltx_plan_failed")
	}
	return parseLTXPlan(result.Stdout, output, requested)
}

func parseLTXPlan(raw []byte, output string, requested uint64) (uint64, error) {
	if len(raw) > 64<<10 {
		return 0, refuse("ltx_plan_failed")
	}
	var plan struct {
		Target  string `json:"target_path"`
		Replica string `json:"replica"`
		MaxTXID string `json:"max_txid"`
	}
	if err := json.Unmarshal(raw, &plan); err != nil || plan.Target != output || plan.Replica != "s3" {
		return 0, refuse("ltx_point_not_exact")
	}
	selected, err := strconv.ParseUint(plan.MaxTXID, 16, 64)
	if err != nil || selected == 0 || plan.MaxTXID != txidString(selected) || requested != 0 && selected != requested {
		return 0, refuse("ltx_point_not_exact")
	}
	return selected, nil
}
