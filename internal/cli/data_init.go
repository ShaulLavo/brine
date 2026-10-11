package cli

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"

	"github.com/ShaulLavo/brine/internal/datainit"
	"github.com/ShaulLavo/brine/internal/dispatch"
	"github.com/ShaulLavo/brine/internal/jobs"
	"github.com/ShaulLavo/brine/internal/ops"
	"github.com/ShaulLavo/brine/internal/result"
	"github.com/spf13/cobra"
)

func newDataInitCmd(deps Dependencies, modes *machineModes) *cobra.Command {
	var flags operationFlags
	var planned, noWait bool
	var id, artifactFile string
	var r datainit.Request
	command := &cobra.Command{Use: "init APP", Short: "Review and initialize never-started empty data with a verified backup", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		r.App = args[0]
		if !dispatch.ValidApp(r.App) || planned && noWait || planned && (id != "" || !datainit.ValidID(r.FirstReleasePlan) || artifactFile == "") || !planned && (!datainit.ValidID(id) || r.FirstReleasePlan != "" || artifactFile != "") {
			return result.New(result.InvalidUsage, nil)
		}
		op := "data_init_apply"
		var request any = dispatch.DataInitApplyArgs{App: r.App, PlanID: id}
		if planned {
			raw, err := readInitializerArtifact(artifactFile)
			if err != nil {
				return result.New(result.InvalidUsage, nil)
			}
			sum := sha256.Sum256(raw)
			r.Artifact = "sha256:" + hex.EncodeToString(sum[:])
			r.ArtifactBytes = raw
			initializer, err := datainit.DecodeArtifact(raw, r.Artifact)
			if err != nil || initializer.Validate(cmd.Context()) != nil {
				return result.New(result.InvalidUsage, nil)
			}
			op, request = "data_init_plan", dispatch.DataInitPlanArgs(r)
		}
		response, err := flags.call(cmd.Context(), deps, op, request)
		if err != nil {
			return err
		}
		value := response.Data
		if !planned {
			accepted, ok := value.(jobs.Accepted)
			if !ok {
				return result.New(result.TransportInvalidResponse, nil)
			}
			value, err = flags.waitTask(cmd.Context(), deps, accepted, ops.DataInitApply, noWait)
			if err != nil {
				return err
			}
			if initialized, ok := value.(datainit.Operation); ok && initialized.PlanID != id {
				return result.New(result.TransportInvalidResponse, nil)
			}
		}
		if modes.enabled() {
			return json.NewEncoder(cmd.OutOrStdout()).Encode(result.Success(cmd.CommandPath(), value))
		}
		if p, ok := value.(datainit.Plan); ok {
			initializer, err := datainit.DecodeArtifact(p.Request.ArtifactBytes, p.Request.Artifact)
			if err != nil {
				return result.New(result.TransportInvalidResponse, nil)
			}
			catalog, err := json.MarshalIndent(p.Catalog, "", "  ")
			if err != nil {
				return err
			}
			if _, err = fmt.Fprintf(cmd.OutOrStdout(), "Initialization plan %s\nArtifact: %s\nCatalog SHA-256: %s\nSQL:\n", p.ID, p.Request.Artifact, p.Definition.CatalogSHA256); err != nil {
				return err
			}
			for _, statement := range initializer.Statements {
				if _, err = fmt.Fprintf(cmd.OutOrStdout(), "%s;\n", statement); err != nil {
					return err
				}
			}
			_, err = fmt.Fprintf(cmd.OutOrStdout(), "Resulting catalog:\n%s\nApply with brine data init %s --plan-id %s --target %s. App code is not started.\n", catalog, r.App, p.ID, flags.target)
			return err
		}
		if accepted, ok := value.(jobs.Accepted); ok {
			_, err = fmt.Fprintf(cmd.OutOrStdout(), "Accepted initialization operation %s. Inspect with %s.\n", accepted.OperationID, flags.command("status", "--operation", accepted.OperationID))
			return err
		}
		initialized, ok := value.(datainit.Operation)
		if !ok {
			return result.New(result.TransportInvalidResponse, nil)
		}
		_, err = fmt.Fprintf(cmd.OutOrStdout(), "Initialization %s: %s. Replan deployment before starting the first compatible release.\n", initialized.ID, initialized.State)
		return err
	}}
	command.Flags().BoolVar(&noWait, "no-wait", false, "Return the accepted job ID without waiting")
	command.Flags().BoolVar(&planned, "plan", false, "Review SQL and catalog without changing schema")
	command.Flags().StringVar(&id, "plan-id", "", "Apply this initialization plan; never replay an attempted mutation")
	command.Flags().StringVar(&r.FirstReleasePlan, "first-release-plan", "", "Exact retained deployment or preparation plan")
	command.Flags().StringVar(&artifactFile, "artifact", "", "Local reviewed initializer JSON exported by brine data schema")
	flags.register(command)
	return command
}

func readInitializerArtifact(filename string) ([]byte, error) {
	info, err := os.Lstat(filename)
	if err != nil || !info.Mode().IsRegular() || info.Size() > datainit.ArtifactLimit {
		return nil, datainit.ErrRefused
	}
	file, err := os.Open(filename) // #nosec G304 -- Explicit client-selected initializer, never a host pathname.
	if err != nil {
		return nil, err
	}
	defer func() { _ = file.Close() }()
	info, err = file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() > datainit.ArtifactLimit {
		return nil, datainit.ErrRefused
	}
	raw, err := io.ReadAll(io.LimitReader(file, datainit.ArtifactLimit+1))
	if err != nil || len(raw) > datainit.ArtifactLimit {
		return nil, datainit.ErrRefused
	}
	return raw, nil
}
