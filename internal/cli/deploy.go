package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/ShaulLavo/brine/internal/backupcredentials"
	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/datainit"
	"github.com/ShaulLavo/brine/internal/dispatch"
	"github.com/ShaulLavo/brine/internal/jobs"
	"github.com/ShaulLavo/brine/internal/ops"
	"github.com/ShaulLavo/brine/internal/plan"
	"github.com/ShaulLavo/brine/internal/result"
	"github.com/ShaulLavo/brine/internal/spec"
	"github.com/ShaulLavo/brine/internal/systemd"
	"github.com/ShaulLavo/brine/internal/transport"
	"github.com/spf13/cobra"
)

type deploymentReview struct {
	Release      *dispatch.Planned        `json:"release,omitempty"`
	App          string                   `json:"app"`
	Image        string                   `json:"image"`
	State        dispatch.DeploymentState `json:"state"`
	Preparation  *dispatch.Planned        `json:"preparation,omitempty"`
	Initializers []data.SchemaInitializer `json:"initializers"`
}
type deploymentReport struct {
	Review deploymentReview `json:"review"`
	Steps  []deploymentStep `json:"steps"`
}
type deploymentFailure struct {
	error
	report deploymentReport
}

func (e *deploymentFailure) Unwrap() error           { return e.error }
func (e *deploymentFailure) machineFailureData() any { return e.report }

func newDeployCmd(deps Dependencies, modes *machineModes, noInput *bool) *cobra.Command {
	var flags operationFlags
	var yes bool
	var credentialFile string
	cmd := &cobra.Command{Use: "deploy <brine.toml> --target NAME", Short: "Review and run data preparation, initialization and release in order", Args: offlineSpecArg, RunE: func(cmd *cobra.Command, args []string) (retErr error) {
		if !flags.validSelection() {
			return result.New(result.InvalidUsage, nil)
		}
		raw, err := readOfflineInput(args[0], dispatch.RequestLimit/2)
		if err != nil {
			return err
		}
		app, err := parseClientSpec(cmd.Context(), args[0], raw, cmd.ErrOrStderr())
		if err != nil {
			return result.New(result.InvalidUsage, err)
		}
		request := dispatch.PlanArgs{Spec: string(raw), SchemaArtifacts: app.SchemaArtifacts}
		target, err := flags.loadTarget(deps)
		if err != nil {
			return err
		}
		identity, err := json.Marshal(struct {
			Target    transport.Target
			Spec      string
			Artifacts []data.SchemaArtifact
		}{target, string(raw), app.SchemaArtifacts})
		if err != nil {
			return err
		}
		resume, err := flags.openDeploymentResume(identity)
		if err != nil {
			return err
		}
		defer resume.close()
		report := deploymentReport{Review: deploymentReview{App: string(app.Name), Image: string(app.Image), Initializers: []data.SchemaInitializer{}}, Steps: resume.journal.Steps}
		defer func() {
			if retErr != nil {
				for _, step := range report.Steps {
					if step.OperationID != "" && !step.Complete {
						_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "Inspect unfinished %s with: %s\n", step.Name, flags.command("status", "--operation", step.OperationID))
						break
					}
				}
				retErr = &deploymentFailure{error: retErr, report: report}
			}
		}()
		for i := range resume.journal.Steps {
			step := &resume.journal.Steps[i]
			if step.Complete || step.OperationID != "" || step.PlanID == "" {
				continue
			}
			kind := ops.Deploy
			if step.Name == "credentials" {
				kind = ops.CredentialActivation
			}
			if step.Name == "initialization" {
				kind = ops.DataInitApply
			}
			if kind == ops.Deploy && step.Key == "" {
				continue
			}
			response, err := flags.call(cmd.Context(), deps, "deploy_operation", dispatch.DeploymentOperationQuery{PlanID: step.PlanID, Kind: kind, Key: step.Key})
			if err != nil {
				return err
			}
			recovered, ok := response.Data.(dispatch.DeploymentOperation)
			if !ok || recovered.OperationID != "" && !jobs.ValidID(recovered.OperationID) {
				return result.New(result.TransportInvalidResponse, nil)
			}
			step.OperationID = recovered.OperationID
			if err = resume.save(); err != nil {
				return err
			}
		}
		// An unfinished task may hold a fence or expose a partial schema. Observe
		// its durable outcome before asking readiness facts to admit fresh work.
		for i := range resume.journal.Steps {
			step := &resume.journal.Steps[i]
			if step.Complete || step.OperationID == "" {
				continue
			}
			retry, err := flags.settledDeploymentFailure(cmd.Context(), deps, *step)
			if err != nil {
				return err
			}
			if retry {
				continue
			}
			if err = flags.waitDeployment(cmd.Context(), deps, *step); err != nil {
				return err
			}
			step.Complete = true
			if err = resume.save(); err != nil {
				return err
			}
		}
		state, err := flags.deploymentState(cmd.Context(), deps, request)
		if err != nil {
			return err
		}
		report.Review.State = state
		if len(state.Databases) != len(app.Databases) {
			return result.New(result.TransportInvalidResponse, nil)
		}
		needsData := false
		missingCredentials := false
		needsPacket := false
		for i, d := range state.Databases {
			if d.Name != string(app.Databases[i].Name) {
				return result.New(result.TransportInvalidResponse, nil)
			}
			needsData = needsData || !d.Initialized
			missingCredentials = missingCredentials || !d.Credentials
			pending := false
			for _, step := range resume.journal.Steps {
				pending = pending || step.Name == "credentials" && step.Database == d.Name && step.OperationID != "" && !step.Complete
			}
			needsPacket = needsPacket || !d.Credentials && !pending
		}
		if needsData && len(app.Initializers) != len(app.Databases) {
			return result.New(result.InvalidUsage, nil)
		}
		var packetBytes []byte
		var packet backupcredentials.Packet
		defer func() { clear(packetBytes); packet.Clear() }()
		if needsData && missingCredentials {
			if credentialFile == "" && needsPacket {
				report.Review.Initializers = app.Initializers
				if !modes.enabled() {
					if err = printDeploymentReview(cmd.Context(), cmd.OutOrStdout(), report.Review, app); err != nil {
						return err
					}
				}
				_, err := fmt.Fprintf(cmd.ErrOrStderr(), "Backup credentials are not delivered. Supply an externally issued 0600 JSON packet; Brine never creates credentials. Run: %s\n", flags.command("deploy", args[0], "--backup-credentials", "credentials.json"))
				if err != nil {
					return err
				}
				return result.New(result.InvalidUsage, nil)
			}
			if credentialFile != "" {
				packetBytes, err = readDeploymentCredentials(credentialFile)
				if err != nil {
					return result.New(result.InvalidUsage, nil)
				}
				packet, err = backupcredentials.DecodePacket(packetBytes)
				if err != nil {
					return result.New(result.InvalidUsage, nil)
				}
			}
		}
		previousComplete := len(resume.journal.Steps) > 0
		for _, step := range resume.journal.Steps {
			previousComplete = previousComplete && step.Complete
		}
		if previousComplete && (needsData || resume.journal.ReviewHash != state.ReviewHash) {
			// A completed workflow is not authority for a later app incarnation
			// or policy. Review the new host facts from scratch.
			resume.journal = deploymentJournal{Steps: []deploymentStep{}}
		}
		if resume.journal.ReviewHash != "" && resume.journal.ReviewHash != state.ReviewHash {
			return result.New(result.Conflict, nil)
		}
		if len(resume.journal.Steps) == 0 {
			resume.journal.Steps = append(resume.journal.Steps, deploymentStep{Name: "preparation"})
			for _, name := range []string{"credentials", "initialization"} {
				for _, database := range app.Databases {
					resume.journal.Steps = append(resume.journal.Steps, deploymentStep{Name: name, Database: string(database.Name)})
				}
			}
			resume.journal.Steps = append(resume.journal.Steps, deploymentStep{Name: "release"})
			resume.journal.ReviewHash = state.ReviewHash
		}
		if len(resume.journal.Steps) != 2+2*len(app.Databases) {
			return result.New(result.InvalidUsage, nil)
		}
		for i := 0; i < len(app.Databases); i++ {
			if resume.journal.Steps[1+i].Name != "credentials" || resume.journal.Steps[1+i].Database != string(app.Databases[i].Name) || resume.journal.Steps[1+len(app.Databases)+i].Name != "initialization" || resume.journal.Steps[1+len(app.Databases)+i].Database != string(app.Databases[i].Name) {
				return result.New(result.InvalidUsage, nil)
			}
		}
		report.Steps = resume.journal.Steps
		if !needsData {
			for i := 0; i < len(report.Steps)-1; i++ {
				if report.Steps[i].OperationID == "" {
					report.Steps[i].Complete = true
				}
			}
		}
		if needsData {
			report.Review.Initializers = app.Initializers
			if resume.journal.Steps[0].PlanID == "" || deploymentNeedsPreparation(state) {
				// Retain the original preparation identities across lost responses.
				step := &resume.journal.Steps[0]
				if step.PlanID == "" {
					p, err := flags.deploymentPlan(cmd.Context(), deps, "data_prepare_plan", request)
					if err != nil {
						return err
					}
					if p.Kind == plan.Conflict {
						return result.New(result.Conflict, nil)
					}
					step.PlanID = p.PlanID
					report.Review.Preparation = &p
					if err = resume.save(); err != nil {
						return err
					}
				} else {
					report.Review.Preparation = &dispatch.Planned{PlanID: step.PlanID, Kind: plan.Create, SchemaArtifacts: app.SchemaArtifacts, Conflicts: []plan.Diagnostic{}}
				}
			}
		} else {
			p, err := flags.deploymentPlan(cmd.Context(), deps, "plan", request)
			report.Review.Release = &p
			if err != nil {
				return err
			}
			if p.Kind == plan.Conflict {
				return result.New(result.Conflict, nil)
			}
			if report.Steps[len(report.Steps)-1].Complete && p.Kind != plan.NoOp {
				report.Steps[len(report.Steps)-1] = deploymentStep{Name: "release", PlanID: p.PlanID}
			}
			if report.Steps[len(report.Steps)-1].OperationID == "" && !report.Steps[len(report.Steps)-1].Complete {
				report.Steps[len(report.Steps)-1].PlanID = p.PlanID
			}
			if p.Kind == plan.NoOp && report.Steps[len(report.Steps)-1].OperationID == "" {
				report.Steps[len(report.Steps)-1].Complete = true
			}
		}
		if !modes.enabled() {
			if err = printDeploymentReview(cmd.Context(), cmd.OutOrStdout(), report.Review, app); err != nil {
				return err
			}
		}
		if !yes {
			if modes.enabled() || *noInput {
				if modes.enabled() {
					return json.NewEncoder(cmd.OutOrStdout()).Encode(result.Success(cmd.CommandPath(), report))
				}
				_, err = fmt.Fprintln(cmd.OutOrStdout(), "No changes applied. Rerun with --yes after reviewing.")
				return err
			}
			if err = RequestInput(cmd, func(ctx context.Context, input io.Reader, output io.Writer) error {
				if _, err := fmt.Fprintln(output, "Type yes to approve these steps:"); err != nil {
					return err
				}
				return readDeploymentApproval(ctx, input)
			}); err != nil {
				return err
			}
		}
		if err = resume.save(); err != nil {
			return err
		}
		runData := needsData
		for _, previous := range resume.journal.Steps[:len(resume.journal.Steps)-1] {
			runData = runData || previous.OperationID != "" && !previous.Complete
		}
		if runData {
			// Observe unfinished detached work before asking live facts to permit a new
			// attempt. Recovery-required work is inspected, never blindly replayed.
			for i := 0; i < len(resume.journal.Steps)-1; i++ {
				step := &resume.journal.Steps[i]
				if step.Complete {
					continue
				}
				if step.OperationID != "" {
					retry, err := flags.settledDeploymentFailure(cmd.Context(), deps, *step)
					if err != nil {
						return err
					}
					if !retry {
						if err = flags.waitDeployment(cmd.Context(), deps, *step); err != nil {
							return err
						}
						step.Complete = true
						if err = resume.save(); err != nil {
							return err
						}
						continue
					}
					fresh, err := flags.deploymentState(cmd.Context(), deps, request)
					if err != nil {
						return err
					}
					if fresh.ReviewHash != report.Review.State.ReviewHash || deploymentFenced(fresh) {
						return result.New(result.RecoveryRequired, nil)
					}
					*step = deploymentStep{Name: step.Name, Database: step.Database}
					if err = resume.save(); err != nil {
						return err
					}
				}
				fresh, err := flags.deploymentState(cmd.Context(), deps, request)
				if err != nil {
					return err
				}
				if fresh.ReviewHash != report.Review.State.ReviewHash {
					return result.New(result.Conflict, nil)
				}
				var db dispatch.DeploymentDatabase
				for _, candidate := range fresh.Databases {
					if candidate.Name == step.Database {
						db = candidate
					}
				}
				if deploymentFenced(fresh) {
					return result.New(result.RecoveryRequired, nil)
				}
				completed := step.Name == "preparation" && !deploymentNeedsPreparation(fresh) || step.Name == "credentials" && db.Credentials || step.Name == "initialization" && db.Initialized
				if completed {
					step.Complete = true
					if err = resume.save(); err != nil {
						return err
					}
					continue
				}
				var op string
				var payload any
				switch step.Name {
				case "preparation":
					if step.PlanID == "" {
						p, err := flags.deploymentPlan(cmd.Context(), deps, "data_prepare_plan", request)
						if err != nil {
							return err
						}
						if p.Kind == plan.Conflict {
							return result.New(result.Conflict, nil)
						}
						step.PlanID = p.PlanID
					}
					op = "apply"
					if step.Key == "" {
						step.Key, err = randomOperationKey()
						if err != nil {
							return err
						}
					}
					payload = dispatch.ApplyArgs{PlanID: step.PlanID, IdempotencyKey: step.Key}
				case "credentials":
					if len(packetBytes) == 0 {
						_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "Backup credentials are required for a fresh attempt. Run: %s\n", flags.command("deploy", args[0], "--backup-credentials", "credentials.json"))
						return result.New(result.InvalidUsage, nil)
					}
					if step.PlanID == "" {
						response, err := flags.call(cmd.Context(), deps, "backup_credentials_plan", dispatch.BackupCredentialPlanArgs{App: string(app.Name), Database: db.Name, ExpiresAt: packet.Expiry()})
						if err != nil {
							return err
						}
						p, ok := response.Data.(backupcredentials.Plan)
						if !ok || !p.Valid() || p.Scope.App != string(app.Name) {
							return result.New(result.TransportInvalidResponse, nil)
						}
						step.PlanID = p.ID
					}
					op = "backup_credentials_set"
					payload, err = dispatch.EncodeBackupCredentialSet(string(app.Name), db.Name, step.PlanID, packetBytes)
					if err != nil {
						return err
					}
				case "initialization":
					if step.PlanID == "" {
						var initializer data.SchemaInitializer
						for _, candidate := range app.Initializers {
							if string(candidate.Definition.Database) == step.Database {
								initializer = candidate
							}
						}
						artifact, bytes, err := initializer.Artifact()
						if err != nil {
							return err
						}
						response, err := flags.call(cmd.Context(), deps, "data_init_plan", dispatch.DataInitPlanArgs{App: string(app.Name), FirstReleasePlan: resume.journal.Steps[0].PlanID, Artifact: artifact.Digest, ArtifactBytes: bytes})
						if err != nil {
							return err
						}
						p, ok := response.Data.(datainit.Plan)
						if !ok || !p.Valid() || p.Definition != artifact.Definition || p.Request.Artifact != artifact.Digest {
							return result.New(result.TransportInvalidResponse, nil)
						}
						step.PlanID = p.ID
					}
					op = "data_init_apply"
					payload = dispatch.DataInitApplyArgs{App: string(app.Name), PlanID: step.PlanID}
				}
				if err = resume.save(); err != nil {
					return err
				}
				response, err := flags.call(cmd.Context(), deps, op, payload)
				if wire, ok := payload.(json.RawMessage); ok {
					clear(wire)
				}
				if err != nil {
					return err
				}
				accepted, ok := response.Data.(jobs.Accepted)
				if !ok || !response.OK || accepted.Status != "accepted" || !jobs.ValidID(accepted.OperationID) {
					return result.New(result.TransportInvalidResponse, nil)
				}
				step.OperationID = accepted.OperationID
				if err = resume.save(); err != nil {
					return err
				}
				if _, err = fmt.Fprintf(cmd.ErrOrStderr(), "%s operation_id=%s\n", step.Name, step.OperationID); err != nil {
					return err
				}
				if err = flags.waitDeployment(cmd.Context(), deps, *step); err != nil {
					return err
				}
				step.Complete = true
				if err = resume.save(); err != nil {
					return err
				}
			}
		}
		step := &resume.journal.Steps[len(resume.journal.Steps)-1]
		if step.OperationID != "" && !step.Complete {
			retry, err := flags.settledDeploymentFailure(cmd.Context(), deps, *step)
			if err != nil {
				return err
			}
			if retry {
				*step = deploymentStep{Name: "release"}
				if err = resume.save(); err != nil {
					return err
				}
			}
		}
		if !step.Complete {
			if step.OperationID == "" {
				fresh, err := flags.deploymentState(cmd.Context(), deps, request)
				if err != nil {
					return err
				}
				if fresh.ReviewHash != report.Review.State.ReviewHash {
					return result.New(result.Conflict, nil)
				}
				if step.PlanID == "" {
					p, err := flags.deploymentPlan(cmd.Context(), deps, "plan", request)
					if err != nil {
						return err
					}
					if p.Kind == plan.Conflict {
						return result.New(result.Conflict, nil)
					}
					step.PlanID = p.PlanID
					if p.Kind == plan.NoOp {
						step.Complete = true
					}
				}
				if !step.Complete {
					if step.Key == "" {
						step.Key, err = randomOperationKey()
						if err != nil {
							return err
						}
					}
					if err = resume.save(); err != nil {
						return err
					}
					response, err := flags.call(cmd.Context(), deps, "apply", dispatch.ApplyArgs{PlanID: step.PlanID, IdempotencyKey: step.Key})
					if err != nil {
						return err
					}
					accepted, ok := response.Data.(jobs.Accepted)
					if !ok || !response.OK || !jobs.ValidID(accepted.OperationID) {
						return result.New(result.TransportInvalidResponse, nil)
					}
					step.OperationID = accepted.OperationID
					if err = resume.save(); err != nil {
						return err
					}
				}
			}
			if !step.Complete {
				if err = flags.waitDeployment(cmd.Context(), deps, *step); err != nil {
					return err
				}
				step.Complete = true
			}
			if err = resume.save(); err != nil {
				return err
			}
		}
		if modes.enabled() {
			return json.NewEncoder(cmd.OutOrStdout()).Encode(result.Success(cmd.CommandPath(), report))
		}
		_, err = fmt.Fprintln(cmd.OutOrStdout(), "Deployment completed. Each step retains its own operation journal.")
		return err
	}}
	flags.register(cmd)
	cmd.Flags().BoolVar(&yes, "yes", false, "Approve the combined review and execute in order")
	cmd.Flags().StringVar(&credentialFile, "backup-credentials", "", "Existing 0600 JSON packet with externally issued backup credentials")
	return cmd
}

func (f operationFlags) deploymentState(ctx context.Context, deps Dependencies, request dispatch.PlanArgs) (dispatch.DeploymentState, error) {
	response, err := f.call(ctx, deps, "deploy_state", request)
	if err != nil {
		return dispatch.DeploymentState{}, err
	}
	state, ok := response.Data.(dispatch.DeploymentState)
	if !response.OK || !ok || !jobs.ValidPlanID(state.ReviewHash) {
		return dispatch.DeploymentState{}, result.New(result.TransportInvalidResponse, nil)
	}
	return state, nil
}
func (f operationFlags) deploymentPlan(ctx context.Context, deps Dependencies, op string, request dispatch.PlanArgs) (dispatch.Planned, error) {
	response, err := f.call(ctx, deps, op, request)
	if err != nil {
		return dispatch.Planned{}, err
	}
	p, ok := response.Data.(dispatch.Planned)
	if !response.OK || !ok || !jobs.ValidPlanID(p.PlanID) {
		return dispatch.Planned{}, result.New(result.TransportInvalidResponse, nil)
	}
	return p, nil
}
func (f operationFlags) waitDeployment(ctx context.Context, deps Dependencies, step deploymentStep) error {
	kind := ops.Deploy
	if step.Name == "credentials" {
		kind = ops.CredentialActivation
	}
	if step.Name == "initialization" {
		kind = ops.DataInitApply
	}
	if kind.IsTask() {
		value, err := f.waitTask(ctx, deps, jobs.Accepted{Status: "accepted", OperationID: step.OperationID}, kind, false)
		if err != nil {
			return err
		}
		switch r := value.(type) {
		case datainit.Operation:
			if r.PlanID != step.PlanID {
				return result.New(result.TransportInvalidResponse, nil)
			}
		case backupcredentials.Receipt:
			if r.PlanID != step.PlanID {
				return result.New(result.TransportInvalidResponse, nil)
			}
		default:
			return result.New(result.TransportInvalidResponse, nil)
		}
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, systemd.JobRuntimeLimit+time.Minute)
	defer cancel()
	for {
		response, err := f.call(ctx, deps, "operation", dispatch.OperationArgs{OperationID: step.OperationID})
		if err != nil {
			return err
		}
		s, ok := response.Data.(jobs.Status)
		if !response.OK || !ok || s.Operation.ID != step.OperationID || s.Operation.Kind != kind {
			return result.New(result.TransportInvalidResponse, nil)
		}
		if s.Operation.PlanID != step.PlanID {
			return result.New(result.TransportInvalidResponse, nil)
		}
		if s.Operation.State == ops.Succeeded {
			return nil
		}
		if s.Operation.State.IsTerminal() {
			return result.New(result.RecoveryRequired, nil)
		}
		timer := time.NewTimer(time.Second)
		select {
		case <-ctx.Done():
			timer.Stop()
			return result.New(result.Interrupted, ctx.Err())
		case <-timer.C:
		}
	}
}
func readDeploymentCredentials(path string) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0600 || info.Size() > backupcredentials.PacketLimit {
		return nil, backupcredentials.ErrInvalid
	}
	file, err := os.Open(path) // #nosec G304 -- Explicit client credential file, admitted by regular-file, identity and 0600 checks.
	if err != nil {
		return nil, err
	}
	defer func() { _ = file.Close() }()
	opened, err := file.Stat()
	if err != nil || !os.SameFile(info, opened) || opened.Mode().Perm() != 0600 {
		return nil, backupcredentials.ErrInvalid
	}
	raw, err := io.ReadAll(io.LimitReader(file, backupcredentials.PacketLimit+1))
	if err != nil || len(raw) > backupcredentials.PacketLimit {
		clear(raw)
		return nil, backupcredentials.ErrInvalid
	}
	return raw, nil
}
func printDeploymentReview(ctx context.Context, w io.Writer, review deploymentReview, app spec.App) error {
	if _, err := fmt.Fprintf(w, "Combined deployment review for %s\n1. Data preparation (no writer or replica starts):\n", review.App); err != nil {
		return err
	}
	for i, d := range review.State.Databases {
		declaration := app.Databases[i]
		state := "allocate and verify"
		if d.Prepared {
			state = "already prepared"
		}
		if _, err := fmt.Fprintf(w, "  %s: %s; root %s; container %s/%s; backup destination %s\n", d.Name, state, declaration.PersistentRoot, declaration.MountPath, declaration.Filename, declaration.BackupDestination); err != nil {
			return err
		}
	}
	if review.Preparation != nil {
		if _, err := fmt.Fprintf(w, "  Preparation plan: %s\n", review.Preparation.PlanID); err != nil {
			return err
		}
	}
	if _, err := fmt.Fprintln(w, "2. Backup credentials (externally issued, never minted):"); err != nil {
		return err
	}
	for _, d := range review.State.Databases {
		state := "delivery required from the 0600 packet"
		if d.Credentials {
			state = "already delivered"
		}
		if _, err := fmt.Fprintf(w, "  %s: %s\n", d.Name, state); err != nil {
			return err
		}
	}
	if _, err := fmt.Fprintln(w, "3. Schema initialization after an independently verified empty restore point:"); err != nil {
		return err
	}
	for _, initializer := range review.Initializers {
		if _, err := fmt.Fprintf(w, "Database %s, marker %s, catalog %s\n", initializer.Definition.Database, initializer.Definition.Marker, initializer.Definition.CatalogSHA256); err != nil {
			return err
		}
		for _, statement := range initializer.Statements {
			if _, err := fmt.Fprintf(w, "%s;\n", statement); err != nil {
				return err
			}
		}
		catalog, err := data.InitializerCatalog(ctx, initializer)
		if err != nil {
			return err
		}
		raw, err := json.MarshalIndent(catalog, "", "  ")
		if err != nil {
			return err
		}
		if _, err = fmt.Fprintf(w, "Resulting catalog:\n%s\n", raw); err != nil {
			return err
		}
	}
	if review.Release != nil {
		if _, err := fmt.Fprintf(w, "Release plan: %s (%s)\n", review.Release.PlanID, review.Release.Kind); err != nil {
			return err
		}
		if review.Release.Diff != nil {
			if err := json.NewEncoder(w).Encode(review.Release.Diff); err != nil {
				return err
			}
		}
	}
	_, err := fmt.Fprintf(w, "4. Release: %s, port %d, domains %v\nReview hash: %s\nDependent immutable plans are built after their prerequisites; policy or desired changes stop execution.\n", review.Image, app.ContainerPort, app.Domains, review.State.ReviewHash)
	return err
}

// Only explicitly settled failures can admit a fresh attempt on a rerun. Live
// affirmative facts and the existing engines still own mutation admission.
func (f operationFlags) settledDeploymentFailure(ctx context.Context, deps Dependencies, step deploymentStep) (bool, error) {
	response, err := f.call(ctx, deps, "operation", dispatch.OperationArgs{OperationID: step.OperationID})
	if err != nil {
		return false, err
	}
	s, ok := response.Data.(jobs.Status)
	if !response.OK || !ok || s.Operation.ID != step.OperationID {
		return false, result.New(result.TransportInvalidResponse, nil)
	}
	kind := ops.Deploy
	if step.Name == "credentials" {
		kind = ops.CredentialActivation
	}
	if step.Name == "initialization" {
		kind = ops.DataInitApply
	}
	if s.Operation.Kind != kind || kind.IsTask() && s.Operation.SecretRef != step.PlanID || !kind.IsTask() && s.Operation.PlanID != step.PlanID {
		return false, result.New(result.TransportInvalidResponse, nil)
	}
	if s.Operation.State == ops.RecoveryRequired {
		return false, result.New(result.RecoveryRequired, nil)
	}
	return s.Operation.State == ops.Failed || s.Operation.State == ops.RolledBack, nil
}

func deploymentNeedsPreparation(s dispatch.DeploymentState) bool {
	for _, d := range s.Databases {
		if !d.Prepared {
			return true
		}
	}
	return false
}
func deploymentFenced(s dispatch.DeploymentState) bool {
	for _, d := range s.Databases {
		if d.Fenced {
			return true
		}
	}
	return false
}
