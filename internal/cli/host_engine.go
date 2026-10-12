package cli

import (
	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/replication"
	"github.com/spf13/cobra"
)

func newHostEngineCommands(deps Dependencies) []*cobra.Command {
	execute := &cobra.Command{Use: "engine-exec BINDING", Hidden: true, Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		if !data.ValidID(args[0]) || deps.HostEngineExec == nil {
			return replication.ErrPermit
		}
		return deps.HostEngineExec(cmd.Context(), args[0])
	}}
	observe := &cobra.Command{Use: "engine-copy-observe", Hidden: true, Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		if deps.HostEngineCopyObserve == nil {
			return replication.ErrPermit
		}
		return deps.HostEngineCopyObserve(cmd.Context(), deps.Stdin, deps.Stdout)
	}}
	return []*cobra.Command{execute, observe}
}
