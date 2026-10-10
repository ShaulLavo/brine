package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/user"
	"path/filepath"
	"time"

	"github.com/ShaulLavo/brine/internal/diagnose"
	"github.com/ShaulLavo/brine/internal/dispatch"
	"github.com/ShaulLavo/brine/internal/inventory"
	"github.com/ShaulLavo/brine/internal/localexec"
	"github.com/ShaulLavo/brine/internal/result"
	"github.com/spf13/cobra"
)

func newHostCmd(deps Dependencies) *cobra.Command {
	host := &cobra.Command{Use: "host", Hidden: true}
	serve := &cobra.Command{Use: "serve", Hidden: true, DisableFlagParsing: true, RunE: func(_ *cobra.Command, _ []string) error { return executeHostServe(deps) }}
	host.AddCommand(serve, newHostEnrollmentCmd(deps), newHostRunOpCmd(deps), newHostReconcileCmd(deps))
	host.AddCommand(newHostPermitCommands(deps)...)
	return host
}

func executeHostServe(deps Dependencies) error {
	return executeHostServeFinalized(deps, nil)
}

func executeHostServeFinalized(deps Dependencies, finalize func(error) error) error {
	fmt.Fprintf(deps.Stderr, "ssh_original_command_length=%d\n", deps.OriginalCommandLength)
	uid := deps.HostUID
	if uid == nil {
		uid = os.Geteuid
	}
	var envelope result.Envelope
	var err error
	if uid() == 0 {
		err = result.New(result.DispatchRootRefused, nil)
		envelope = result.Failure("brine host serve", err)
	} else {
		ctx, cancel := context.WithTimeout(deps.Context, 15*time.Second)
		defer cancel()
		if closer, ok := deps.Stdin.(io.Closer); ok {
			stopClose := context.AfterFunc(ctx, func() { _ = closer.Close() })
			defer stopClose()
		}
		collector := deps.HostInventory
		if collector == nil {
			identity, e := user.LookupId(fmt.Sprint(uid()))
			if e == nil && identity.Username == "brine" {
				key, e := os.ReadFile("/etc/ssh/brine/inventory-key")
				if e == nil && len(key) == 32 {
					collector = inventory.Collector{FS: inventory.HostFS{}, Runner: localexec.ExecRunner{}, IdentityKey: key}
				}
			}
		}
		server := dispatch.NewServer(deps.Version, collector).WithJobs(deps.HostJobs, deps.HostAuthorization)
		server.Factory = deps.HostServerFactory
		server.Reconciler = deps.HostReconciler
		server.Planner = deps.HostPlanner
		server.Apps = deps.HostApps
		server.Config = deps.HostConfig
		server.Secrets = deps.HostSecrets
		server.BackupCredentials = deps.HostBackupCredentials
		server.RestoreTests = deps.HostRestoreTests
		if deps.HostLogs != nil {
			server.Logs = deps.HostLogs
		}
		server.Diagnose = deps.HostDiagnose
		if server.Diagnose == nil {
			reader := diagnose.Reader{Inventory: collector, Logs: deps.HostLogs, Runner: localexec.ExecRunner{}, FS: inventory.HostFS{}}
			if identity, e := user.LookupId(fmt.Sprint(uid())); e == nil && identity.Username == "brine" {
				reader.Store = diagnose.DiskStore{Dir: filepath.Join(identity.HomeDir, ".local/state/brine")}
			}
			server.Diagnose = reader
		}
		envelope, err = server.Handle(ctx, deps.Stdin)
	}
	if finalize != nil {
		err = finalize(err)
		if err != nil {
			envelope = result.Failure(envelope.Command, err)
		}
	}
	if writeErr := json.NewEncoder(deps.Stdout).Encode(envelope); writeErr != nil {
		return result.New(result.InternalError, writeErr)
	}
	return err
}

// HostServeRequested resolves the command before interpreting machine flags.
// Setup and execution share this predicate so leading global flags cannot select
// a dispatcher path that missed the process safeguards.
func HostServeRequested(args []string) bool {
	root := NewRootCommand(Dependencies{Context: context.Background()})
	command, _, err := root.Find(args)
	return err == nil && command != nil && command.CommandPath() == "brine host serve"
}

func HostRuntimeRequested(args []string) bool {
	root := NewRootCommand(Dependencies{Context: context.Background()})
	command, _, err := root.Find(args)
	return err == nil && command != nil && (command.CommandPath() == "brine host serve" || command.CommandPath() == "brine host run-op" || command.CommandPath() == "brine host reconcile")
}

// HostStartupRequested selects only the startup service bundle, never deployment managers.
func HostStartupRequested(args []string) bool {
	root := NewRootCommand(Dependencies{Context: context.Background()})
	command, _, err := root.Find(args)
	if err != nil || command == nil {
		return false
	}
	switch command.CommandPath() {
	case "brine host writer-permit", "brine host writer-attempt", "brine host replica-permit", "brine host replica-exec":
		return true
	default:
		return false
	}
}
