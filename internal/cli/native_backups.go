package cli

import (
	"encoding/json"
	"fmt"

	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/dispatch"
	"github.com/ShaulLavo/brine/internal/jobs"
	"github.com/ShaulLavo/brine/internal/nativecapture"
	"github.com/ShaulLavo/brine/internal/ops"
	"github.com/ShaulLavo/brine/internal/result"
	"github.com/spf13/cobra"
)

type nativeTaskFlags struct {
	operation operationFlags
	key       string
	noWait    bool
}

func (f *nativeTaskFlags) register(cmd *cobra.Command) {
	f.operation.register(cmd)
	cmd.Flags().StringVar(&f.key, "idempotency-key", "", "Reuse the reported key to recover the same operation after a lost response")
	cmd.Flags().BoolVar(&f.noWait, "no-wait", false, "Return the accepted operation ID without waiting for verification")
}

func (f *nativeTaskFlags) prepareKey(cmd *cobra.Command) error {
	if !f.operation.validSelection() || cmd.Flags().Changed("idempotency-key") && f.key == "" || f.key != "" && !data.ValidID(f.key) {
		return result.New(result.InvalidUsage, nil)
	}
	if f.key != "" {
		return nil
	}
	key, err := randomOperationKey()
	if err != nil {
		return err
	}
	f.key = key
	if _, err := fmt.Fprintf(cmd.ErrOrStderr(), "idempotency_key=%s\n", key); err != nil {
		return result.New(result.InternalError, err)
	}
	return nil
}

func newNativeBackupCmd(deps Dependencies, modes *machineModes, kind ops.Kind) *cobra.Command {
	var flags nativeTaskFlags
	var database string
	name, short := "capture", "Capture and verify a SpacetimeDB backup without stopping writers"
	if kind == ops.NativePrepare {
		name, short = "prepare", "Verify a fresh exact SpacetimeDB point and retain its data fence"
	}
	cmd := &cobra.Command{Use: name + " APP --database NAME --target NAME", Short: short, Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		if !dispatch.ValidApp(args[0]) || !dispatch.ValidApp(database) {
			return result.New(result.InvalidUsage, nil)
		}
		if err := flags.prepareKey(cmd); err != nil {
			return err
		}
		capture := dispatch.NativeCaptureArgs{App: args[0], Database: database, IdempotencyKey: flags.key}
		var input any = capture
		if kind == ops.NativePrepare {
			input = dispatch.NativePrepareArgs(capture)
		}
		return flags.run(cmd, deps, modes, kind, input, "")
	}}
	flags.register(cmd)
	cmd.Flags().StringVar(&database, "database", "", "Managed database name (required)")
	return cmd
}

func newNativeRestoreTestCmd(deps Dependencies, modes *machineModes) *cobra.Command {
	var flags nativeTaskFlags
	cmd := &cobra.Command{Use: "native-test POINT_ID --target NAME", Short: "Verify an exact SpacetimeDB backup in private isolation without changing live data", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		if !data.ValidID(args[0]) {
			return result.New(result.InvalidUsage, nil)
		}
		if err := flags.prepareKey(cmd); err != nil {
			return err
		}
		input := dispatch.NativeRestoreTestArgs{PointID: args[0], IdempotencyKey: flags.key}
		return flags.run(cmd, deps, modes, ops.NativeRestoreTest, input, args[0])
	}}
	flags.register(cmd)
	return cmd
}

func newNativeAbandonCmd(deps Dependencies, modes *machineModes) *cobra.Command {
	var flags nativeTaskFlags
	cmd := &cobra.Command{Use: "abandon POINT_ID --target NAME", Short: "Abandon an unused exact native preparation and leave its components stopped", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		if !data.ValidID(args[0]) {
			return result.New(result.InvalidUsage, nil)
		}
		if err := flags.prepareKey(cmd); err != nil {
			return err
		}
		input := dispatch.NativeAbandonArgs{PointID: args[0], IdempotencyKey: flags.key}
		return flags.run(cmd, deps, modes, ops.NativePrepareAbandon, input, args[0])
	}}
	flags.register(cmd)
	return cmd
}

func (f nativeTaskFlags) run(cmd *cobra.Command, deps Dependencies, modes *machineModes, kind ops.Kind, input any, pointID string) error {
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
	if kind == ops.NativePrepareAbandon {
		receipt, ok := value.(nativecapture.AbandonReceipt)
		if !ok || receipt.PointID != pointID {
			return result.New(result.TransportInvalidResponse, nil)
		}
		if modes.enabled() {
			return json.NewEncoder(cmd.OutOrStdout()).Encode(result.Success(cmd.CommandPath(), receipt))
		}
		_, err := fmt.Fprintf(cmd.OutOrStdout(), "Preparation %s abandoned. Its original fence is released; engine, frontend and ingress remain stopped.\n", receipt.PointID)
		return err
	}
	receipt, ok := value.(nativecapture.TaskReceipt)
	if !ok || pointID != "" && receipt.PointID != pointID {
		return result.New(result.TransportInvalidResponse, nil)
	}
	if modes.enabled() {
		return json.NewEncoder(cmd.OutOrStdout()).Encode(result.Success(cmd.CommandPath(), receipt))
	}
	_, err = fmt.Fprintf(cmd.OutOrStdout(), "SpacetimeDB %s verified. Point %s in epoch %s.\n", nativeTaskName(kind), receipt.PointID, receipt.EpochID)
	if err != nil || kind != ops.NativePrepare {
		return err
	}
	_, err = fmt.Fprintln(cmd.OutOrStdout(), "The data fence remains held for the reviewed data operation.")
	return err
}

func nativeTaskName(kind ops.Kind) string {
	if kind == ops.NativePrepare {
		return "exact preparation"
	}
	if kind == ops.NativeRestoreTest {
		return "restore test"
	}
	return "capture"
}
