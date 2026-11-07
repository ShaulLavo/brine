package cli

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/ShaulLavo/brine/internal/dispatch"
	"github.com/ShaulLavo/brine/internal/result"
	"github.com/ShaulLavo/brine/internal/sqlitestatus"
	"github.com/spf13/cobra"
)

func newSQLiteBackupStatusCmd(deps Dependencies, modes *machineModes) *cobra.Command {
	var flags operationFlags
	var database string
	command := &cobra.Command{Use: "status APP --target NAME", Short: "Observe SQLite backup health with bounded read-only remote probes", Long: "Inspect SQLite replica service, remote access, remote restore watermark, credential expiry, lifecycle evidence and independently verified history. A running service or remote watermark is not proof of durability. Local position, lag and possible loss remain unknown. This command never stops writers, starts replication or prepares a recovery point.", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		a := dispatch.SQLiteBackupStatusArgs{App: args[0], Database: database}
		if !a.Valid() || !flags.validSelection() {
			return result.New(result.InvalidUsage, nil)
		}
		response, err := flags.call(cmd.Context(), deps, "sqlite_backup_status", a)
		if err != nil {
			return err
		}
		status, ok := response.Data.(sqlitestatus.Status)
		if !ok || !response.OK || !status.Valid() || status.App != a.App || a.Database != "" && status.Database != a.Database {
			return result.New(result.TransportInvalidResponse, nil)
		}
		if modes.enabled() {
			return json.NewEncoder(cmd.OutOrStdout()).Encode(result.Success(cmd.CommandPath(), status))
		}
		return printSQLiteStatus(cmd, status)
	}}
	flags.register(command)
	command.Flags().StringVar(&database, "database", "", "SQLite database name (required when more than one is present)")
	return command
}
func printSQLiteStatus(cmd *cobra.Command, s sqlitestatus.Status) error {
	watermark := "unknown"
	if s.Watermark.TXID != nil {
		watermark = fmt.Sprintf("%016x", *s.Watermark.TXID)
	}
	verified := "none"
	if s.LastVerified != nil {
		verified = s.LastVerified.VerifiedAt.Format("2006-01-02T15:04:05Z07:00") + " (historical, not current durability)"
	}
	remaining := "unknown"
	if s.Credential.RemainingSeconds != nil {
		remaining = fmt.Sprintf("%d seconds", *s.Credential.RemainingSeconds)
	}
	_, err := fmt.Fprintf(cmd.OutOrStdout(), "SQLite backups %s for %s/%s, observed %s.\nService: %s at %s. Remote: %s at %s. Watermark: %s at %s.\nLast independently verified: %s.\nCredentials: %s, version %d, received %s, expires %s, remaining %s. Renewal needed: %t.\nLifecycle: %s, verified %s, fresh until %s. Held fences: %d.\nLocal position, lag and possible loss: unknown. Running is not durability.\nReasons: %s.\n", s.State, s.App, s.Database, s.ObservedAt.Format(time.RFC3339), s.Service.State, s.Service.ObservedAt.Format(time.RFC3339), s.Remote.State, s.Remote.ObservedAt.Format(time.RFC3339), watermark, s.Watermark.ObservedAt.Format(time.RFC3339), verified, s.Credential.State, s.Credential.Version, sqliteStatusTime(s.Credential.ReceivedAt), sqliteStatusTime(s.Credential.ExpiresAt), remaining, s.Credential.RenewalNeeded, s.Lifecycle.State, sqliteStatusTime(s.Lifecycle.VerifiedAt), sqliteStatusTime(s.Lifecycle.FreshUntil), len(s.HeldFences), strings.Join(s.Reasons, ", "))
	if err != nil {
		return err
	}
	for _, fence := range s.HeldFences {
		if _, err := fmt.Fprintf(cmd.OutOrStdout(), "Held fence %s, owner operation %s.\n", fence.ID, fence.OwnerOperation); err != nil {
			return err
		}
	}
	return nil
}
func sqliteStatusTime(at *time.Time) string {
	if at == nil {
		return "unknown"
	}
	return at.Format(time.RFC3339)
}
