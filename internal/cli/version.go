package cli

import (
	"encoding/json"
	"fmt"

	"github.com/ShaulLavo/brine/internal/result"
	"github.com/spf13/cobra"
)

func newVersionCmd(jsonOutput *bool, version string) *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Show the CLI version",
		RunE: func(cmd *cobra.Command, args []string) error {
			if *jsonOutput {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(result.Success(cmd.CommandPath(), map[string]any{
					"version": version,
				}))
			}
			fmt.Fprintln(cmd.OutOrStdout(), "brine "+version)
			return nil
		},
	}
}
