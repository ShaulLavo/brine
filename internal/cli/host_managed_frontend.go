package cli

import (
	"context"
	"time"

	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/dispatch"
	"github.com/ShaulLavo/brine/internal/replication"
	"github.com/spf13/cobra"
)

func newHostManagedFrontendPermit(deps Dependencies) *cobra.Command {
	return &cobra.Command{Use: "managed-frontend-permit BINDING APP", Hidden: true, Args: cobra.ExactArgs(2), RunE: func(cmd *cobra.Command, args []string) error {
		if !data.ValidID(args[0]) || !dispatch.ValidApp(args[1]) || deps.HostManagedFrontendPermit == nil {
			return replication.ErrPermit
		}
		ctx, cancel := context.WithTimeout(cmd.Context(), 30*time.Second)
		defer cancel()
		return deps.HostManagedFrontendPermit(ctx, args[0], args[1])
	}}
}

type lazyManagedFrontend struct{ runtime *invocationRuntime }

func (p lazyManagedFrontend) Permit(ctx context.Context, binding, app string) error {
	if err := p.runtime.initializeStartup(ctx, p.runtime.lifecycle.OpenManagedFrontendPermit); err != nil {
		return err
	}
	if p.runtime.services.ManagedFrontendPermit == nil {
		return replication.ErrPermit
	}
	return p.runtime.services.ManagedFrontendPermit(ctx, binding, app)
}
