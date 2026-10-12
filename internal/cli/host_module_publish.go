package cli

import (
	"github.com/ShaulLavo/brine/internal/datapublish"
	"github.com/spf13/cobra"
)

func newHostModuleDescribe(deps Dependencies) *cobra.Command {
	return &cobra.Command{Use: "module-describe", Hidden: true, Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		if deps.HostModuleDescribe == nil {
			return datapublish.ErrRecord
		}
		return deps.HostModuleDescribe(cmd.Context(), deps.Stdin, deps.Stdout)
	}}
}
