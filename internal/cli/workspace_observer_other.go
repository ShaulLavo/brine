//go:build !linux

package cli

import (
	"github.com/ShaulLavo/brine/internal/result"
	"github.com/spf13/cobra"
)

func newHostWorkspaceObserverCmd(Dependencies) *cobra.Command {
	return &cobra.Command{Use: "workspace-observer", Hidden: true, Args: cobra.NoArgs, RunE: func(*cobra.Command, []string) error { return result.New(result.DependencyMissing, nil) }}
}
