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

func newHostModuleDescribeProfile(deps Dependencies) *cobra.Command {
	return &cobra.Command{Use: "module-describe-profile", Hidden: true, Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		if deps.HostModuleDescribeProfile == nil {
			return datapublish.ErrRecord
		}
		return deps.HostModuleDescribeProfile(cmd.Context(), deps.Stdin, deps.Stdout)
	}}
}

func newHostModuleStage(deps Dependencies) *cobra.Command {
	return &cobra.Command{Use: "module-stage", Hidden: true, Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		if deps.HostModuleStage == nil {
			return datapublish.ErrRecord
		}
		return deps.HostModuleStage(cmd.Context(), deps.Stdin, deps.Stdout)
	}}
}

func newHostEngineMountProfile(deps Dependencies) *cobra.Command {
	return &cobra.Command{Use: "engine-mount-profile", Hidden: true, Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		if deps.HostEngineMountProfile == nil {
			return datapublish.ErrRecord
		}
		return deps.HostEngineMountProfile(cmd.Context(), deps.Stdout)
	}}
}
