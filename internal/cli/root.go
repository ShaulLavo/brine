package cli

import (
	"context"
	"io"

	"github.com/spf13/cobra"
)

const Version = "0.1.0-dev"

// Dependencies supplies the process resources and services used by commands.
// Callers provide every field and own error reporting and process exit status.
type Dependencies struct {
	Context  context.Context
	Stdin    io.Reader
	Stdout   io.Writer
	Stderr   io.Writer
	Version  string
	LookPath func(string) (string, error)
	RunTUI   func(context.Context, io.Reader, io.Writer) error
}

// NewRootCommand builds an independent command tree without executing it.
// Execute returns command errors unchanged so callers can classify them.
func NewRootCommand(deps Dependencies) *cobra.Command {
	var jsonOutput bool
	var noInput bool

	root := &cobra.Command{
		Use:           "brine",
		Short:         "Agent-first self-hosted deployments",
		Long:          "A small deployment control plane for humans and agents. Deployment operations are not implemented yet.",
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	root.SetContext(deps.Context)
	root.SetIn(deps.Stdin)
	root.SetOut(deps.Stdout)
	root.SetErr(deps.Stderr)
	root.PersistentFlags().BoolVar(&jsonOutput, "json", false, "Machine-readable JSON output")
	root.PersistentFlags().BoolVar(&noInput, "no-input", false, "Never request interactive input")
	root.AddCommand(newDoctorCmd(&jsonOutput, deps.LookPath))
	root.AddCommand(newVersionCmd(&jsonOutput, deps.Version))
	root.AddCommand(newTUICmd(&jsonOutput, &noInput, deps.RunTUI))
	return root
}
