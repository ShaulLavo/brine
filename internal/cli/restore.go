package cli

import (
	"encoding/json"
	"fmt"

	"github.com/ShaulLavo/brine/internal/dispatch"
	"github.com/ShaulLavo/brine/internal/restore"
	"github.com/ShaulLavo/brine/internal/result"
	"github.com/ShaulLavo/brine/internal/transport"
	"github.com/spf13/cobra"
)

func newRestoreCmd(deps Dependencies, modes *machineModes) *cobra.Command {
	root := &cobra.Command{Use: "restore", Short: "Verify remote backups without changing live application data"}
	var flags operationFlags
	var database, txid, point string
	test := &cobra.Command{Use: "test APP --target NAME", Short: "Restore into an isolated private workspace and check declared data invariants", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		request := dispatch.RestoreTestArgs{App: args[0], Database: database, TXID: txid, Point: point}
		if !request.Valid() || !transport.ValidTargetName(flags.target) {
			return result.New(result.InvalidUsage, nil)
		}
		response, err := flags.call(cmd.Context(), deps, "restore_test", request)
		if err != nil {
			return err
		}
		receipt, ok := response.Data.(restore.Receipt)
		if !ok || !response.OK || !receipt.Valid() {
			return result.New(result.TransportInvalidResponse, nil)
		}
		if modes.enabled() {
			return json.NewEncoder(cmd.OutOrStdout()).Encode(result.Success(cmd.CommandPath(), receipt))
		}
		_, err = fmt.Fprintf(cmd.OutOrStdout(), "Restore verified for %s. Integrity: %s; foreign keys: %s; declared checks: %s. Schema: %s (%s). Possible loss: %s. %s\n", args[0], receipt.IntegrityCheck, receipt.ForeignKeyCheck, receipt.InvariantCheck, receipt.Schema.Marker, receipt.Schema.State, receipt.LossWindow.State, receipt.LossWindow.Reason)
		return err
	}}
	test.Flags().StringVar(&database, "database", "", "Database name (required for apps with multiple databases)")
	test.Flags().StringVar(&txid, "txid", "", "Exact nonzero hexadecimal Litestream transaction ID")
	test.Flags().StringVar(&point, "point", "", "Durable immutable restore-point ID")
	test.MarkFlagsMutuallyExclusive("txid", "point")
	flags.register(test)
	root.AddCommand(test)
	return root
}
