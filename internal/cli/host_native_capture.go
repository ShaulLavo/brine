package cli

import (
	"context"
	"encoding/json"

	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/jobs"
	"github.com/ShaulLavo/brine/internal/result"
	"github.com/spf13/cobra"
)

func newHostNativeCaptureCmd(deps Dependencies) *cobra.Command {
	return &cobra.Command{Use: "native-capture BINDING_ID", Hidden: true, Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		if !data.ValidID(args[0]) {
			return result.New(result.InvalidUsage, nil)
		}
		if deps.HostNativeCapture == nil {
			return result.New(result.DependencyMissing, nil)
		}
		accepted, err := deps.HostNativeCapture(cmd.Context(), args[0])
		if err != nil {
			return err
		}
		return json.NewEncoder(cmd.OutOrStdout()).Encode(result.Success(cmd.CommandPath(), accepted))
	}}
}

// HostNativeCaptureRequested constructs command callbacks but never executes them.
func HostNativeCaptureRequested(ctx context.Context, args []string) bool {
	root := NewRootCommand(Dependencies{Context: ctx}) //nolint:contextcheck // This predicate only resolves a command path; no callback runs.
	cmd, _, err := root.Find(args)
	return err == nil && cmd != nil && cmd.CommandPath() == "brine host native-capture"
}
func (r lazyNativeCapture) Capture(ctx context.Context, binding string) (jobs.Accepted, error) {
	if err := r.runtime.initialize(ctx, false); err != nil {
		return jobs.Accepted{}, err
	}
	if r.runtime.services.NativeCapture == nil {
		return jobs.Accepted{}, result.New(result.DependencyMissing, nil)
	}
	return r.runtime.services.NativeCapture(ctx, binding)
}

type lazyNativeCapture struct{ runtime *invocationRuntime }
