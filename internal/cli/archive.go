package cli

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/ShaulLavo/brine/internal/archive"
	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/dispatch"
	"github.com/ShaulLavo/brine/internal/jobs"
	"github.com/ShaulLavo/brine/internal/result"
	"github.com/spf13/cobra"
)

// ArchiveOperations is the enrolled local archive workflow, not remote removal.
// Interrupted attempts require inspection and keep their durable data fences.
type ArchiveOperations interface {
	Plan(context.Context, string) (archive.Plan, error)
	Prepare(context.Context, string, string) (archive.Manifest, error)
	Retire(context.Context, data.ArchiveID) (archive.Disposition, error)
	Abandon(context.Context, data.ArchiveID) (archive.Disposition, error)
}

func newArchivePrepareCmd(deps Dependencies) *cobra.Command {
	var planned bool
	var id string
	command := &cobra.Command{Use: "archive-prepare APP", Hidden: true, Short: "Prepare immutable archive evidence; do not remove or rename data", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		if !dispatch.ValidApp(args[0]) || planned && id != "" || !planned && !jobs.ValidPlanID(id) {
			return result.New(result.InvalidUsage, nil)
		}
		if deps.HostArchives == nil {
			return result.New(result.DependencyMissing, nil)
		}
		var value any
		var err error
		if planned {
			value, err = deps.HostArchives.Plan(cmd.Context(), args[0])
		} else {
			value, err = deps.HostArchives.Prepare(cmd.Context(), args[0], id)
		}
		if err != nil {
			if errors.Is(err, archive.ErrRecovery) {
				return result.New(result.RecoveryRequired, nil)
			}
			return result.New(result.PolicyRefused, nil)
		}
		return json.NewEncoder(cmd.OutOrStdout()).Encode(result.Success(cmd.CommandPath(), value))
	}}
	command.Flags().BoolVar(&planned, "plan", false, "Record immutable archive intent without stopping processes")
	command.Flags().StringVar(&id, "plan-id", "", "Prepare or inspect this exact retained plan; never replay an interrupted attempt")
	return command
}

type lazyArchives struct{ runtime *invocationRuntime }

func (l lazyArchives) service(ctx context.Context) (archive.Service, error) {
	if err := l.runtime.initialize(ctx, false); err != nil {
		return archive.Service{}, err
	}
	return l.runtime.services.Archives, nil
}
func (l lazyArchives) Plan(ctx context.Context, app string) (archive.Plan, error) {
	s, err := l.service(ctx)
	if err != nil {
		return archive.Plan{}, err
	}
	return s.Plan(ctx, app)
}
func (l lazyArchives) Prepare(ctx context.Context, app, id string) (archive.Manifest, error) {
	s, err := l.service(ctx)
	if err != nil {
		return archive.Manifest{}, err
	}
	return s.Prepare(ctx, app, id)
}
func HostArchiveRequested(ctx context.Context, args []string) bool {
	root := NewRootCommand(Dependencies{Context: ctx}) //nolint:contextcheck // Metadata-only command lookup does not execute a handler.
	command, _, err := root.Find(args)
	return err == nil && command != nil && archiveCommandPath(command.CommandPath())
}

func (l lazyArchives) Retire(ctx context.Context, id data.ArchiveID) (archive.Disposition, error) {
	s, err := l.service(ctx)
	if err != nil {
		return archive.Disposition{}, err
	}
	return s.Retire(ctx, id)
}
func (l lazyArchives) Abandon(ctx context.Context, id data.ArchiveID) (archive.Disposition, error) {
	s, err := l.service(ctx)
	if err != nil {
		return archive.Disposition{}, err
	}
	return s.Abandon(ctx, id)
}
func newArchiveDispositionCmd(deps Dependencies, abandon bool) *cobra.Command {
	name := "archive-retire"
	short := "Retire a prepared archive by immutable ID"
	if abandon {
		name = "archive-abandon"
		short = "Abandon a prepared archive and restore managed startup permission"
	}
	return &cobra.Command{Use: name + " ARCHIVE_ID", Hidden: true, Short: short, Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		if !data.ValidID(args[0]) {
			return result.New(result.InvalidUsage, nil)
		}
		if deps.HostArchives == nil {
			return result.New(result.DependencyMissing, nil)
		}
		var value archive.Disposition
		var err error
		if abandon {
			value, err = deps.HostArchives.Abandon(cmd.Context(), data.ArchiveID(args[0]))
		} else {
			value, err = deps.HostArchives.Retire(cmd.Context(), data.ArchiveID(args[0]))
		}
		if err != nil {
			if errors.Is(err, archive.ErrRecovery) {
				return result.New(result.RecoveryRequired, nil)
			}
			return result.New(result.PolicyRefused, nil)
		}
		return json.NewEncoder(cmd.OutOrStdout()).Encode(result.Success(cmd.CommandPath(), value))
	}}
}

func (l lazyArchives) Inspect(ctx context.Context, id data.ArchiveID) (archive.Inspection, error) {
	s, err := l.service(ctx)
	if err != nil {
		return archive.Inspection{}, err
	}
	return s.Inspect(ctx, id)
}
func (l lazyArchives) Reconcile(ctx context.Context, id data.ArchiveID) (archive.Disposition, error) {
	s, err := l.service(ctx)
	if err != nil {
		return archive.Disposition{}, err
	}
	return s.Reconcile(ctx, id)
}
func newArchiveReconcileCmd(deps Dependencies) *cobra.Command {
	var inspect bool
	command := &cobra.Command{Use: "archive-reconcile ARCHIVE_ID", Hidden: true, Short: "Inspect or settle interrupted archive retirement by immutable ID", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		if !data.ValidID(args[0]) {
			return result.New(result.InvalidUsage, nil)
		}
		service, ok := deps.HostArchives.(interface {
			Inspect(context.Context, data.ArchiveID) (archive.Inspection, error)
			Reconcile(context.Context, data.ArchiveID) (archive.Disposition, error)
		})
		if !ok {
			return result.New(result.DependencyMissing, nil)
		}
		var value any
		var err error
		if inspect {
			value, err = service.Inspect(cmd.Context(), data.ArchiveID(args[0]))
		} else {
			value, err = service.Reconcile(cmd.Context(), data.ArchiveID(args[0]))
		}
		if err != nil {
			if errors.Is(err, archive.ErrRecovery) {
				return result.New(result.RecoveryRequired, nil)
			}
			return result.New(result.PolicyRefused, nil)
		}
		return json.NewEncoder(cmd.OutOrStdout()).Encode(result.Success(cmd.CommandPath(), value))
	}}
	command.Flags().BoolVar(&inspect, "inspect", false, "Read journal and effect identities without changing runtime or data")
	return command
}
