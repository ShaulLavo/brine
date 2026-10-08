package cli

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

const version = "0.1.0-dev"

func Execute() error {
	var jsonOutput bool
	var noInput bool

	root := &cobra.Command{
		Use:           "deployctl",
		Short:         "Agent-first self-hosted deployments",
		Long:          "A small deployment control plane for humans and agents. Deployment operations are not implemented yet.",
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	root.PersistentFlags().BoolVar(&jsonOutput, "json", false, "Machine-readable JSON output")
	root.PersistentFlags().BoolVar(&noInput, "no-input", false, "Never request interactive input")
	root.AddCommand(newDoctorCmd(&jsonOutput))
	root.AddCommand(newVersionCmd(&jsonOutput))
	root.AddCommand(newTUICmd(&jsonOutput, &noInput))

	if err := root.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return err
	}
	return nil
}
