package cli

import (
	"encoding/json"
	"fmt"

	"github.com/spf13/cobra"
)

func newVersionCmd(jsonOutput *bool) *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Show the CLI version",
		RunE: func(cmd *cobra.Command, args []string) error {
			if *jsonOutput {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(map[string]any{
					"schema_version": 1,
					"version":        version,
				})
			}
			fmt.Fprintln(cmd.OutOrStdout(), "deployctl "+version)
			return nil
		},
	}
}
