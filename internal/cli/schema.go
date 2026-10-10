package cli

import (
	"fmt"
	"io"

	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/dispatch"
	"github.com/ShaulLavo/brine/internal/result"
	"github.com/ShaulLavo/brine/internal/spec"
	"github.com/spf13/cobra"
)

func newSchemaCmd() *cobra.Command {
	var database string
	cmd := &cobra.Command{Use: "schema <brine.toml> --database NAME", Short: "Write the derived initializer JSON for operator review and installation", Args: offlineSpecArg, RunE: func(cmd *cobra.Command, args []string) error {
		raw, err := readOfflineInput(args[0], dispatch.RequestLimit/2)
		if err != nil {
			return err
		}
		app, err := spec.ParseFile(cmd.Context(), args[0], raw)
		if err != nil {
			return result.New(result.InvalidUsage, err)
		}
		for _, initializer := range app.Initializers {
			if string(initializer.Definition.Database) != database {
				continue
			}
			_, raw, err := initializer.Artifact()
			if err != nil {
				return err
			}
			_, err = cmd.OutOrStdout().Write(raw)
			return err
		}
		return result.New(result.InvalidUsage, nil)
	}}
	cmd.Flags().StringVar(&database, "database", "", "Declared database whose initializer to export")
	return cmd
}

func printSchemaArtifacts(w io.Writer, artifacts []data.SchemaArtifact) error {
	for _, artifact := range artifacts {
		if _, err := fmt.Fprintf(w, "Schema initializer %s: %s (marker %s)\n", artifact.Definition.Database, artifact.Digest, artifact.Definition.Marker); err != nil {
			return err
		}
	}
	return nil
}
