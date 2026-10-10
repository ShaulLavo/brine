package cli

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/ShaulLavo/brine/internal/backupcredentials"
	"github.com/ShaulLavo/brine/internal/dispatch"
	"github.com/ShaulLavo/brine/internal/result"
	"github.com/ShaulLavo/brine/internal/transport"
	"github.com/spf13/cobra"
)

func newBackupCmd(deps Dependencies, modes *machineModes) *cobra.Command {
	backup := &cobra.Command{Use: "backup", Short: "Manage backup credential delivery"}
	credentials := &cobra.Command{Use: "credentials", Short: "Plan and deliver externally issued S3 credentials"}
	var planFlags, setFlags operationFlags
	var expiryText, planID, planDatabase, setDatabase string
	planned := &cobra.Command{Use: "plan APP --target NAME", Short: "Plan a private credential version without reading credential values", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		if !dispatch.ValidApp(args[0]) || planDatabase != "" && !dispatch.ValidApp(planDatabase) || !transport.ValidTargetName(planFlags.target) {
			return result.New(result.InvalidUsage, nil)
		}
		var expiry *time.Time
		if expiryText != "" {
			t, err := time.Parse(time.RFC3339Nano, expiryText)
			if err != nil || expiryText[len(expiryText)-1] != 'Z' {
				return result.New(result.InvalidUsage, nil)
			}
			expiry = &t
		}
		response, err := planFlags.call(cmd.Context(), deps, "backup_credentials_plan", dispatch.BackupCredentialPlanArgs{App: args[0], Database: planDatabase, ExpiresAt: expiry})
		if err != nil {
			return err
		}
		p, ok := response.Data.(backupcredentials.Plan)
		if !ok || !response.OK || !p.Valid() || p.Scope.App != args[0] {
			return result.New(result.TransportInvalidResponse, nil)
		}
		if modes.enabled() {
			return json.NewEncoder(cmd.OutOrStdout()).Encode(result.Success(cmd.CommandPath(), p))
		}
		databaseFlag := ""
		if planDatabase != "" {
			databaseFlag = " --database " + planDatabase
		}
		_, err = fmt.Fprintf(cmd.OutOrStdout(), "Credential plan %s, version %d. Deliver with brine backup credentials set %s --plan-id %s --target %s%s. Delivery stores a private version and activates only its committed database replica.\n", p.ID, p.Version, args[0], p.ID, planFlags.target, databaseFlag)
		return err
	}}
	planned.Flags().StringVar(&planDatabase, "database", "", "Database name (required for apps with multiple databases)")
	planned.Flags().StringVar(&expiryText, "expires-at", "", "Optional issuer-supplied UTC expiry (RFC3339 with Z)")
	planFlags.register(planned)
	set := &cobra.Command{Use: "set APP --plan-id ID --target NAME", Short: "Deliver a bounded private JSON credential packet on stdin", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		if !dispatch.ValidApp(args[0]) || setDatabase != "" && !dispatch.ValidApp(setDatabase) || !backupcredentials.ValidPlanID(planID) || !transport.ValidTargetName(setFlags.target) || deps.Stdin == nil || isTerminal(deps.Stdin) {
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
		wire, err := dispatch.EncodeBackupCredentialSet(args[0], setDatabase, planID, raw)
		if err != nil {
			return result.New(result.InvalidUsage, nil)
		}
		defer clear(wire)
		response, err := setFlags.call(cmd.Context(), deps, "backup_credentials_set", wire)
		if err != nil {
			return err
		}
		r, ok := response.Data.(backupcredentials.Receipt)
		if !ok || !response.OK || !r.Valid() || r.PlanID != planID || r.Scope.App != args[0] {
			return result.New(result.TransportInvalidResponse, nil)
		}
		if modes.enabled() {
			return json.NewEncoder(cmd.OutOrStdout()).Encode(result.Success(cmd.CommandPath(), r))
		}
		status := r.ActivationStatus
		if status == "" {
			status = "stored"
		}
		health := r.Health(time.Now().UTC(), time.Minute)
		if r.CredentialHealth != nil {
			health = *r.CredentialHealth
		}
		expiry := "not supplied"
		if r.ExpiresAt != nil {
			expiry = r.ExpiresAt.Format(time.RFC3339)
		}
		_, err = fmt.Fprintf(cmd.OutOrStdout(), "Stored private backup credential version %d. Replica activation: %s. Credential age: %d seconds; issuer expiry: %s. The application was not restarted.\n", r.Version, status, health.AgeSeconds, expiry)
		return err
	}}
	set.Flags().StringVar(&setDatabase, "database", "", "Database name (required for apps with multiple databases)")
	set.Flags().StringVar(&planID, "plan-id", "", "Confirmed credential plan identity")
	setFlags.register(set)
	credentials.AddCommand(planned, set)
	backup.AddCommand(credentials)
	return backup
}
