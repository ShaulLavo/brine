package cli

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/ShaulLavo/brine/internal/archive"
	"github.com/ShaulLavo/brine/internal/backupcredentials"
	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/jobs"
	"github.com/ShaulLavo/brine/internal/result"
	"github.com/spf13/cobra"
)

type ArchiveMaintenanceOperations interface {
	Plan(context.Context, data.ArchiveID, archive.MaintenanceKind) (archive.MaintenancePlan, error)
	Apply(context.Context, data.ArchiveID, string) (archive.MaintenanceOutcome, error)
	ReconcileDelete(context.Context, data.ArchiveID) (archive.Deletion, error)
	Status(context.Context, data.ArchiveID) (archive.MaintenanceStatus, error)
}
type ArchiveCredentialOperations interface {
	Plan(context.Context, string, string, string, *time.Time) (backupcredentials.ArchivePlan, error)
	DeliverScoped(context.Context, string, string, string, string, backupcredentials.Packet) (backupcredentials.ArchiveReceipt, error)
}

func archiveMaintenanceResult(cmd *cobra.Command, value any, err error) error {
	if err != nil {
		if errors.Is(err, archive.ErrRecovery) || errors.Is(err, backupcredentials.ErrStorage) {
			return result.New(result.RecoveryRequired, nil)
		}
		return result.New(result.PolicyRefused, nil)
	}
	return json.NewEncoder(cmd.OutOrStdout()).Encode(result.Success(cmd.CommandPath(), value))
}

func newArchiveMaintainCmd(deps Dependencies) *cobra.Command {
	var planned bool
	var id, kind string
	command := &cobra.Command{Use: "archive-maintain ARCHIVE_ID", Hidden: true, Short: "Review retention verification or local deletion, then apply the exact saved plan", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		validKind := kind == string(archive.VerifyRetention) || kind == string(archive.PurgeArchive) || kind == string(archive.ExpireArchive)
		if !data.ValidID(args[0]) || planned && (id != "" || !validKind) || !planned && (!jobs.ValidPlanID(id) || kind != "") {
			return result.New(result.InvalidUsage, nil)
		}
		if deps.HostArchiveMaintenance == nil {
			return result.New(result.DependencyMissing, nil)
		}
		if planned {
			value, err := deps.HostArchiveMaintenance.Plan(cmd.Context(), data.ArchiveID(args[0]), archive.MaintenanceKind(kind))
			return archiveMaintenanceResult(cmd, value, err)
		}
		value, err := deps.HostArchiveMaintenance.Apply(cmd.Context(), data.ArchiveID(args[0]), id)
		return archiveMaintenanceResult(cmd, value, err)
	}}
	command.Flags().BoolVar(&planned, "plan", false, "Record exact immutable maintenance intent without deleting data or contacting the provider")
	command.Flags().StringVar(&kind, "kind", "", "Plan verify_retention, purge_archive (deliberate early local deletion), or expire_archive")
	command.Flags().StringVar(&id, "plan-id", "", "Apply this exact reviewed plan for this archive ID")
	return command
}

func newArchiveMaintenanceReadCmd(deps Dependencies, reconcile bool) *cobra.Command {
	name, short := "archive-status", "Read retained archive health without refreshing restore evidence"
	if reconcile {
		name, short = "archive-purge-reconcile", "Reconcile only already journaled local archive deletion"
	}
	return &cobra.Command{Use: name + " ARCHIVE_ID", Hidden: true, Short: short, Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		if !data.ValidID(args[0]) {
			return result.New(result.InvalidUsage, nil)
		}
		if deps.HostArchiveMaintenance == nil {
			return result.New(result.DependencyMissing, nil)
		}
		if reconcile {
			value, err := deps.HostArchiveMaintenance.ReconcileDelete(cmd.Context(), data.ArchiveID(args[0]))
			return archiveMaintenanceResult(cmd, value, err)
		}
		value, err := deps.HostArchiveMaintenance.Status(cmd.Context(), data.ArchiveID(args[0]))
		return archiveMaintenanceResult(cmd, value, err)
	}}
}

func newArchiveCredentialsCmd(deps Dependencies) *cobra.Command {
	var planned bool
	var id, expiryText string
	command := &cobra.Command{Use: "archive-credentials ARCHIVE_ID BINDING_ID EPOCH_ID", Hidden: true, Short: "Renew original archived replica credentials without activating a replica", Args: cobra.ExactArgs(3), RunE: func(cmd *cobra.Command, args []string) error {
		for _, arg := range args {
			if !data.ValidID(arg) {
				return result.New(result.InvalidUsage, nil)
			}
		}
		if planned && id != "" || !planned && (!backupcredentials.ValidPlanID(id) || expiryText != "") {
			return result.New(result.InvalidUsage, nil)
		}
		var expiry *time.Time
		if expiryText != "" {
			t, err := time.Parse(time.RFC3339Nano, expiryText)
			if err != nil || !strings.HasSuffix(expiryText, "Z") {
				return result.New(result.InvalidUsage, nil)
			}
			expiry = &t
		}
		if deps.HostArchiveCredentials == nil {
			return result.New(result.DependencyMissing, nil)
		}
		if planned {
			value, err := deps.HostArchiveCredentials.Plan(cmd.Context(), args[0], args[1], args[2], expiry)
			return archiveMaintenanceResult(cmd, value, err)
		}
		if deps.Stdin == nil || isTerminal(deps.Stdin) {
			return result.New(result.InvalidUsage, nil)
		}
		raw, err := readSecretInput(cmd.Context(), deps.Stdin)
		defer clear(raw)
		if canceled := cmd.Context().Err(); canceled != nil {
			return canceled
		}
		if err != nil || len(raw) > backupcredentials.PacketLimit {
			return result.New(result.InvalidUsage, nil)
		}
		packet, err := backupcredentials.DecodePacket(raw)
		if err != nil {
			return result.New(result.InvalidUsage, nil)
		}
		defer packet.Clear()
		value, err := deps.HostArchiveCredentials.DeliverScoped(cmd.Context(), args[0], args[1], args[2], id, packet)
		return archiveMaintenanceResult(cmd, value, err)
	}}
	command.Flags().BoolVar(&planned, "plan", false, "Review renewal without reading secrets or activating a replica")
	command.Flags().StringVar(&id, "plan-id", "", "Deliver private JSON on stdin for this exact archived binding/epoch plan")
	command.Flags().StringVar(&expiryText, "expires-at", "", "Optional issuer-supplied UTC expiry for the reviewed plan")
	return command
}

type lazyArchiveMaintenance struct{ runtime *invocationRuntime }

func (l lazyArchiveMaintenance) service(ctx context.Context) (archive.MaintenanceService, error) {
	if err := l.runtime.initialize(ctx, false); err != nil {
		return archive.MaintenanceService{}, err
	}
	return l.runtime.services.ArchiveMaintenance, nil
}
func (l lazyArchiveMaintenance) Plan(ctx context.Context, id data.ArchiveID, kind archive.MaintenanceKind) (archive.MaintenancePlan, error) {
	s, err := l.service(ctx)
	if err != nil {
		return archive.MaintenancePlan{}, err
	}
	return s.Plan(ctx, id, kind)
}
func (l lazyArchiveMaintenance) Apply(ctx context.Context, archiveID data.ArchiveID, id string) (archive.MaintenanceOutcome, error) {
	s, err := l.service(ctx)
	if err != nil {
		return archive.MaintenanceOutcome{}, err
	}
	return s.Apply(ctx, archiveID, id)
}
func (l lazyArchiveMaintenance) ReconcileDelete(ctx context.Context, id data.ArchiveID) (archive.Deletion, error) {
	s, err := l.service(ctx)
	if err != nil {
		return archive.Deletion{}, err
	}
	return s.ReconcileDelete(ctx, id)
}
func (l lazyArchiveMaintenance) Status(ctx context.Context, id data.ArchiveID) (archive.MaintenanceStatus, error) {
	s, err := l.service(ctx)
	if err != nil {
		return archive.MaintenanceStatus{}, err
	}
	return s.Status(ctx, id)
}

type lazyArchiveCredentials struct{ runtime *invocationRuntime }

func (l lazyArchiveCredentials) service(ctx context.Context) (backupcredentials.ArchiveService, error) {
	if err := l.runtime.initialize(ctx, false); err != nil {
		return backupcredentials.ArchiveService{}, err
	}
	return l.runtime.services.ArchiveCredentials, nil
}
func (l lazyArchiveCredentials) Plan(ctx context.Context, archiveID, binding, epoch string, expiry *time.Time) (backupcredentials.ArchivePlan, error) {
	s, err := l.service(ctx)
	if err != nil {
		return backupcredentials.ArchivePlan{}, err
	}
	return s.Plan(ctx, archiveID, binding, epoch, expiry)
}
func (l lazyArchiveCredentials) DeliverScoped(ctx context.Context, archiveID, binding, epoch, id string, packet backupcredentials.Packet) (backupcredentials.ArchiveReceipt, error) {
	s, err := l.service(ctx)
	if err != nil {
		return backupcredentials.ArchiveReceipt{}, err
	}
	return s.DeliverScoped(ctx, archiveID, binding, epoch, id, packet)
}

func archiveCommandPath(path string) bool {
	switch path {
	case "brine host archive-prepare", "brine host archive-retire", "brine host archive-abandon", "brine host archive-reconcile", "brine host archive-maintain", "brine host archive-status", "brine host archive-purge-reconcile", "brine host archive-credentials":
		return true
	default:
		return false
	}
}
