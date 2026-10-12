//go:build linux

package cli

import (
	"github.com/ShaulLavo/brine/internal/workspaceobserve"
	"github.com/spf13/cobra"
)

func newHostWorkspaceObserverCmd(deps Dependencies) *cobra.Command {
	return &cobra.Command{Use: "workspace-observer", Hidden: true, Args: cobra.NoArgs, RunE: func(command *cobra.Command, _ []string) error { return workspaceobserve.Serve(command.Context()) }}
}
