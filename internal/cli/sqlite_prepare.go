package cli

import (
	"encoding/json"
	"fmt"

	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/dispatch"
	"github.com/ShaulLavo/brine/internal/jobs"
	"github.com/ShaulLavo/brine/internal/ops"
	"github.com/ShaulLavo/brine/internal/result"
	"github.com/ShaulLavo/brine/internal/sqliteprep"
	"github.com/spf13/cobra"
)

const sqlitePrepareLong = `This command STOPS the app's writers, then obtains and independently verifies an exact remote recovery point for one SQLite database. A durable data fence is recorded before writers stop and remains held after success. While it is held, the app's and the replica's startup permits refuse, so both stay stopped, including across a reboot, until the preparation is abandoned.

The replica is stopped as part of preparation and is never started or restarted by it. Use --supersedes PREPARATION_ID to replace an unconsumed preparation under the same fence without starting anything; it re-verifies retained coverage, takes a new barrier from a replica that is still running, or captures a new snapshot of an unchanged stopped source.

For an unreleased source, pass both --declaration-plan PLAN_ID and --credential-version N. A successor may inherit those exact references. Neither option activates replication.

A prepared point is admissible for ten minutes, clipped by recorded retention. Its fence never expires on its own. Check it with: brine restore test APP --point PREPARATION_ID.`

const sqliteAbandonLong = `Release the fence of an unused or failed SQLite preparation after checking that the stopped source is locally unchanged and settled. Abandon starts nothing. The app and its replica start again only on the next explicit start, deploy or reboot, through their normal startup permits.`

func newSQLitePrepareCmd(deps Dependencies, modes *machineModes) *cobra.Command {
	var flags nativeTaskFlags
	var database, supersedes, declaration string
	var version uint64
	cmd := &cobra.Command{Use: "prepare APP --target NAME", Short: "Stop writers and verify an exact fenced SQLite recovery point", Long: sqlitePrepareLong, Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		if !dispatch.ValidApp(args[0]) || database != "" && !dispatch.ValidApp(database) || supersedes != "" && !data.ValidID(supersedes) {
			return result.New(result.InvalidUsage, nil)
		}
		if err := flags.prepareKey(cmd); err != nil {
			return err
		}
		input := dispatch.SQLitePrepareArgs{App: args[0], Database: database, Supersedes: supersedes, IdempotencyKey: flags.key, DeclarationPlanID: declaration, DeliveredCredentialVersion: version}
		if !input.Valid() || cmd.Flags().Changed("declaration-plan") != cmd.Flags().Changed("credential-version") || cmd.Flags().Changed("credential-version") && version == 0 {
			return result.New(result.InvalidUsage, nil)
		}
		return flags.runSQLite(cmd, deps, modes, ops.SQLitePrepare, input, args[0])
	}}
	flags.register(cmd)
	cmd.Flags().StringVar(&database, "database", "", "SQLite database name (required when more than one is present)")
	cmd.Flags().StringVar(&supersedes, "supersedes", "", "Unconsumed preparation ID to replace under the same fence")
	cmd.Flags().StringVar(&declaration, "declaration-plan", "", "Exact retained declaration plan for an unreleased SQLite source")
	cmd.Flags().Uint64Var(&version, "credential-version", 0, "Exact delivered credential version for an unreleased SQLite source")
	return cmd
}

func newSQLiteAbandonCmd(deps Dependencies, modes *machineModes) *cobra.Command {
	var flags nativeTaskFlags
	cmd := &cobra.Command{Use: "abandon PREPARATION_ID --target NAME", Short: "Release an unused SQLite preparation's fence without starting anything", Long: sqliteAbandonLong, Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		if !data.ValidID(args[0]) {
			return result.New(result.InvalidUsage, nil)
		}
		if err := flags.prepareKey(cmd); err != nil {
			return err
		}
		input := dispatch.SQLiteAbandonArgs{PreparationID: args[0], IdempotencyKey: flags.key}
		return flags.runSQLite(cmd, deps, modes, ops.SQLitePrepareAbandon, input, "")
	}}
	flags.register(cmd)
	return cmd
}

func (f nativeTaskFlags) runSQLite(cmd *cobra.Command, deps Dependencies, modes *machineModes, kind ops.Kind, input any, app string) error {
	response, err := f.operation.call(cmd.Context(), deps, string(kind), input)
	if err != nil {
		return err
	}
	accepted, ok := response.Data.(jobs.Accepted)
	if !response.OK || !ok || accepted.Status != "accepted" || !jobs.ValidID(accepted.OperationID) {
		return result.New(result.TransportInvalidResponse, nil)
	}
	if !f.noWait {
		if _, err := fmt.Fprintf(cmd.ErrOrStderr(), "operation_id=%s\n", accepted.OperationID); err != nil {
			return result.New(result.InternalError, err)
		}
	}
	value, err := f.operation.waitTask(cmd.Context(), deps, accepted, kind, f.noWait)
	if err != nil {
		return err
	}
	if f.noWait {
		if modes.enabled() {
			return json.NewEncoder(cmd.OutOrStdout()).Encode(result.Success(cmd.CommandPath(), value))
		}
		_, err = fmt.Fprintf(cmd.OutOrStdout(), "Accepted operation %s. Check with %s.\n", accepted.OperationID, f.operation.command("status", "--operation", accepted.OperationID))
		return err
	}
	receipt, ok := value.(sqliteprep.Receipt)
	if !ok || receipt.Validate() != nil || receipt.Task.Kind != sqliteprep.TaskKind(kind) || receipt.Task.OperationID != accepted.OperationID || app != "" && receipt.Task.App != app {
		return result.New(result.TransportInvalidResponse, nil)
	}
	if modes.enabled() {
		return json.NewEncoder(cmd.OutOrStdout()).Encode(result.Success(cmd.CommandPath(), receipt))
	}
	return printSQLitePreparation(cmd, receipt)
}

func printSQLitePreparation(cmd *cobra.Command, r sqliteprep.Receipt) error {
	if r.Outcome == sqliteprep.AbandonedOutcome {
		_, err := fmt.Fprintf(cmd.OutOrStdout(), "Preparation %s abandoned. Fence %s is released. Nothing was started; the app and its replica start on the next explicit start, deploy or reboot.\n", r.PreparationID, r.Fence.ID)
		return err
	}
	_, err := fmt.Fprintf(cmd.OutOrStdout(), "Preparation %s verified source point %s at %s. Writers and the replica are stopped and fence %s remains held. Abandon it with: brine restore abandon %s\n", r.PreparationID, r.SourcePointID, r.CompletedAt.Format("2006-01-02T15:04:05Z07:00"), r.Fence.ID, r.PreparationID)
	return err
}
