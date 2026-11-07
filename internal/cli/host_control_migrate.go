package cli

import (
	"context"
	"regexp"

	"github.com/ShaulLavo/brine/internal/admission"
	"github.com/ShaulLavo/brine/internal/result"
	"github.com/spf13/cobra"
)

type hostControlKey struct{}

type hostControlPorts struct {
	migrate func(context.Context, admission.Exemption) error
	udpHeld func(context.Context, string) (bool, error)
}

var controlToken = regexp.MustCompile(`^[a-f0-9]{32}$`)

func newHostControlMigrateCmd() *cobra.Command {
	return &cobra.Command{Use: "control-migrate update ID migrating ATTEMPT", Hidden: true, Args: cobra.ExactArgs(4), RunE: runControlMigrate}
}

func runControlMigrate(cmd *cobra.Command, args []string) error {
	if args[0] != string(admission.Update) || args[2] != string(admission.Migrating) || !controlToken.MatchString(args[1]) || !controlToken.MatchString(args[3]) {
		return result.New(result.InvalidUsage, nil)
	}
	ports, _ := cmd.Context().Value(hostControlKey{}).(hostControlPorts)
	if ports.migrate == nil {
		return result.New(result.DependencyMissing, nil)
	}
	return ports.migrate(cmd.Context(), admission.Exemption{Kind: admission.Update, ID: args[1], Stage: admission.Migrating, Attempt: args[3]})
}
