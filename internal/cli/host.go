package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/user"
	"time"

	"github.com/ShaulLavo/brine/internal/dispatch"
	"github.com/ShaulLavo/brine/internal/inventory"
	"github.com/ShaulLavo/brine/internal/localexec"
	"github.com/ShaulLavo/brine/internal/result"
	"github.com/spf13/cobra"
)

func newHostCmd(deps Dependencies) *cobra.Command {
	host := &cobra.Command{Use: "host", Hidden: true}
	serve := &cobra.Command{Use: "serve", Hidden: true, DisableFlagParsing: true, RunE: func(_ *cobra.Command, _ []string) error { return executeHostServe(deps) }}
	host.AddCommand(serve, newHostEnrollmentCmd(deps), newHostRunOpCmd(deps))
	return host
}

func executeHostServe(deps Dependencies) error {
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
		envelope, err = dispatch.NewServer(deps.Version, collector).WithJobs(deps.HostJobs, deps.HostAuthorization).Handle(ctx, deps.Stdin)
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
