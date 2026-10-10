package cli

import (
	"context"
	"time"

	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/replication"
	"github.com/spf13/cobra"
)

// Permits are read-only. The separate attempt command uses only its narrow
// composition, never the full mutating host runtime.
func newHostPermitCommands(deps Dependencies) []*cobra.Command {
	writer := &cobra.Command{Use: "writer-permit INCARNATION", Hidden: true, Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		ctx, cancel := context.WithTimeout(cmd.Context(), 5*time.Second)
		defer cancel()
		return replication.WriterPermit(ctx, deps.HostPermits, args[0])
	}}
	replica := &cobra.Command{Use: "replica-permit DATABASE BINDING EPOCH CONFIG_HASH", Hidden: true, Args: cobra.ExactArgs(4), RunE: func(cmd *cobra.Command, args []string) error {
		ctx, cancel := context.WithTimeout(cmd.Context(), 5*time.Second)
		defer cancel()
		return replication.ReplicaPermit(ctx, deps.HostPermits, permitRequest(args))
	}}
	execute := &cobra.Command{Use: "replica-exec DATABASE BINDING EPOCH CONFIG_HASH CONFIG_PATH CREDENTIAL_PATH", Hidden: true, Args: cobra.ExactArgs(6), RunE: func(cmd *cobra.Command, args []string) error {
		ctx, cancel := context.WithTimeout(cmd.Context(), 5*time.Second)
		defer cancel()
		return replication.ReplicaExec(ctx, deps.HostPermits, replication.ReplicaExecRequest{ReplicaPermitRequest: permitRequest(args), ConfigPath: args[4], CredentialPath: args[5]}, replaceReplicaProcess)
	}}
	attempt := &cobra.Command{Use: "writer-attempt INCARNATION", Hidden: true, Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		ctx, cancel := context.WithTimeout(cmd.Context(), 5*time.Second)
		defer cancel()
		if !data.ValidID(args[0]) || deps.HostWriterAttempt == nil {
			return replication.ErrPermit
		}
		return deps.HostWriterAttempt(ctx, args[0])
	}}
	return []*cobra.Command{writer, attempt, replica, execute}
}
func permitRequest(args []string) replication.ReplicaPermitRequest {
	return replication.ReplicaPermitRequest{DatabaseID: args[0], BindingID: args[1], EpochID: args[2], ConfigHash: args[3]}
}
