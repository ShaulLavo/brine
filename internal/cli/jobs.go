package cli

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/ShaulLavo/brine/internal/dispatch"
	"github.com/ShaulLavo/brine/internal/jobs"
	"github.com/ShaulLavo/brine/internal/ops"
	"github.com/ShaulLavo/brine/internal/result"
	"github.com/ShaulLavo/brine/internal/systemd"
	"github.com/ShaulLavo/brine/internal/transport"
	"github.com/spf13/cobra"
)

type OperationClient interface {
	Call(context.Context, transport.Target, dispatch.Request) (result.Envelope, error)
}
type OperationRunner interface {
	Run(context.Context, string) error
}

type operationFlags struct{ target, configDir string }

func (f *operationFlags) register(cmd *cobra.Command) {
	cmd.Flags().StringVar(&f.target, "target", "", "Pinned target name")
	cmd.Flags().StringVar(&f.configDir, "config-dir", "", "Private client target directory")
}
func (f operationFlags) call(ctx context.Context, deps Dependencies, op string, args any) (result.Envelope, error) {
	if !transport.ValidTargetName(f.target) {
		return result.Envelope{}, result.New(result.InvalidUsage, nil)
	}
	dir := f.configDir
	if dir == "" {
		base, err := os.UserConfigDir()
		if err != nil {
			return result.Envelope{}, err
		}
		dir = filepath.Join(base, "brine", "targets")
	}
	load := deps.LoadOperationTarget
	if load == nil {
		load = loadOperationTarget
	}
	target, err := load(dir, f.target)
	if err != nil {
		return result.Envelope{}, err
	}
	client := deps.OperationClient
	if client == nil {
		client = transport.Client{KnownHostsDir: filepath.Join(dir, "pins")}
	}
	requestID, err := randomOperationKey()
	if err != nil {
		return result.Envelope{}, err
	}
	raw, err := json.Marshal(args)
	if err != nil {
		return result.Envelope{}, err
	}
	return client.Call(ctx, target, dispatch.Request{SchemaVersion: dispatch.SchemaVersion, Op: op, RequestID: requestID, Args: raw})
}
func loadOperationTarget(dir, name string) (transport.Target, error) {
	target, err := transport.LoadTarget(filepath.Join(dir, name+".json"))
	if err != nil {
		return transport.Target{}, err
	}
	if target.Name != name {
		return transport.Target{}, result.New(result.TransportInvalidTarget, nil)
	}
	return target, nil
}
func randomOperationKey() (string, error) {
	var bytes [16]byte
	if _, err := rand.Read(bytes[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(bytes[:]), nil
}
func newApplyCmd(machine *bool, deps Dependencies) *cobra.Command {
	return newAcceptanceCmd(machine, deps, "apply")
}
func newResolveCmd(machine *bool, deps Dependencies) *cobra.Command {
	return newAcceptanceCmd(machine, deps, "resolve")
}
func newAcceptanceCmd(machine *bool, deps Dependencies, verb string) *cobra.Command {
	var flags operationFlags
	var key string
	argument, description := "PLAN_ID", "Accept a plan for detached execution on the target"
	valid := jobs.ValidPlanID
	if verb == "resolve" {
		argument, description = "OPERATION_ID", "Inspect and resolve a terminal recovery-required operation"
		valid = jobs.ValidID
	}
	cmd := &cobra.Command{Use: verb + " " + argument + " --target NAME", Short: description, Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		if !valid(args[0]) || !transport.ValidTargetName(flags.target) || (key != "" && !jobs.ValidID(key)) {
			return result.New(result.InvalidUsage, nil)
		}
		if cmd.Flags().Changed("idempotency-key") && key == "" {
			return result.New(result.InvalidUsage, nil)
		}
		if key == "" {
			var err error
			key, err = randomOperationKey()
			if err != nil {
				return err
			}
			// Generated keys are safe correlation IDs, not request values or secrets.
			if _, err := fmt.Fprintf(deps.Stderr, "idempotency_key=%s\n", key); err != nil {
				return result.New(result.InternalError, err)
			}
		}
		var request any = dispatch.ApplyArgs{PlanID: args[0], IdempotencyKey: key}
		if verb == "resolve" {
			request = dispatch.ResolveArgs{OperationID: args[0], IdempotencyKey: key}
		}
		response, err := flags.call(cmd.Context(), deps, verb, request)
		if err != nil {
			return err
		}
		accepted, ok := response.Data.(jobs.Accepted)
		if !response.OK || !ok || accepted.Status != "accepted" || !jobs.ValidID(accepted.OperationID) {
			return result.New(result.TransportInvalidResponse, nil)
		}
		if *machine {
			return json.NewEncoder(cmd.OutOrStdout()).Encode(result.Success(cmd.CommandPath(), accepted))
		}
		_, err = fmt.Fprintf(cmd.OutOrStdout(), "Accepted operation %s. Check with brine status --operation %s --target %s.\n", accepted.OperationID, accepted.OperationID, flags.target)
		return err
	}}
	flags.register(cmd)
	cmd.Flags().StringVar(&key, "idempotency-key", "", "Reuse this key to recover the same operation after a lost response")
	return cmd
}
func newOperationStatusCmd(machine *bool, modes *machineModes, deps Dependencies) *cobra.Command {
	var flags operationFlags
	var id string
	var cursor uint64
	cmd := &cobra.Command{Use: "status [APP] --target NAME", Short: "Read app releases and health, or poll an operation", Args: cobra.MaximumNArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		if !cmd.Flags().Changed("operation") {
			if cmd.Flags().Changed("after-cursor") {
				return result.New(result.InvalidUsage, nil)
			}
			return appStatus(cmd, deps, flags, *modes, args)
		}
		if len(args) != 0 {
			return result.New(result.InvalidUsage, nil)
		}
		if !jobs.ValidID(id) || !transport.ValidTargetName(flags.target) {
			return result.New(result.InvalidUsage, nil)
		}
		response, err := flags.call(cmd.Context(), deps, "operation", dispatch.OperationArgs{OperationID: id, AfterCursor: cursor})
		if err != nil {
			return err
		}
		status, ok := response.Data.(jobs.Status)
		if !response.OK || !ok || status.Operation.ID != id {
			return result.New(result.TransportInvalidResponse, nil)
		}
		previous := cursor
		for _, event := range status.Events {
			if event.Sequence <= previous {
				return result.New(result.TransportInvalidResponse, nil)
			}
			previous = event.Sequence
		}
		if status.NextCursor != previous {
			return result.New(result.TransportInvalidResponse, nil)
		}
		if *machine {
			return json.NewEncoder(cmd.OutOrStdout()).Encode(result.Success(cmd.CommandPath(), status))
		}
		_, err = fmt.Fprintf(cmd.OutOrStdout(), "Operation %s: %s\nEvents: %d; next cursor: %d\n", id, status.Operation.State, len(status.Events), status.NextCursor)
		if err == nil && status.Operation.State == ops.RecoveryRequired {
			_, err = fmt.Fprintf(cmd.OutOrStdout(), "Inspect and resolve with: brine resolve %s --target %s\n", id, flags.target)
		}
		return err
	}}
	flags.register(cmd)
	cmd.Flags().StringVar(&id, "operation", "", "Accepted operation ID")
	cmd.Flags().Uint64Var(&cursor, "after-cursor", 0, "Return events after this sequence number")
	return cmd
}
func newHostRunOpCmd(deps Dependencies) *cobra.Command {
	return &cobra.Command{Use: "run-op OPERATION_ID", Hidden: true, Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		uid := deps.HostUID
		if uid == nil {
			uid = os.Geteuid
		}
		if uid() == 0 {
			return result.New(result.DispatchRootRefused, nil)
		}
		if !jobs.ValidID(args[0]) {
			return result.New(result.InvalidUsage, nil)
		}
		if deps.HostOperationRunner == nil {
			return result.New(result.DependencyMissing, nil)
		}
		ctx, cancel := context.WithTimeout(cmd.Context(), systemd.JobRuntimeLimit)
		defer cancel()
		return deps.HostOperationRunner.Run(ctx, args[0])
	}}
}
