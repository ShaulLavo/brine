package cli

import (
	"encoding/json"
	"fmt"

	"github.com/ShaulLavo/brine/internal/dispatch"
	"github.com/ShaulLavo/brine/internal/nativecapture"
	"github.com/ShaulLavo/brine/internal/result"
	"github.com/spf13/cobra"
)

func newNativeBackupStatusCmd(deps Dependencies, modes *machineModes) *cobra.Command {
	var flags operationFlags
	var database string
	command := &cobra.Command{Use: "status APP --database NAME --target NAME", Short: "Inspect protected native backup health without probing the provider", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		if !dispatch.ValidApp(args[0]) || !dispatch.ValidApp(database) || !flags.validSelection() {
			return result.New(result.InvalidUsage, nil)
		}
		response, err := flags.call(cmd.Context(), deps, "native_backup_status", dispatch.NativeBackupStatusArgs{App: args[0], Database: database})
		if err != nil {
			return err
		}
		status, ok := response.Data.(nativecapture.BackupStatus)
		if !response.OK || !ok || !status.Valid() {
			return result.New(result.TransportInvalidResponse, nil)
		}
		if modes.enabled() {
			return json.NewEncoder(cmd.OutOrStdout()).Encode(result.Success(cmd.CommandPath(), status))
		}
		_, err = fmt.Fprintf(cmd.OutOrStdout(), "SpacetimeDB backups %s, capture every %d seconds, loss unknown.\n", status.State, status.CaptureIntervalSeconds)
		return err
	}}
	flags.register(command)
	command.Flags().StringVar(&database, "database", "", "Managed database name (required)")
	return command
}
