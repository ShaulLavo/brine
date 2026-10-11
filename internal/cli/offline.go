package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/ShaulLavo/brine/internal/dispatch"
	"github.com/ShaulLavo/brine/internal/plan"
	"github.com/ShaulLavo/brine/internal/planfile"
	"github.com/ShaulLavo/brine/internal/planview"
	"github.com/ShaulLavo/brine/internal/policy"
	"github.com/ShaulLavo/brine/internal/result"
	"github.com/ShaulLavo/brine/internal/target"
	"github.com/spf13/cobra"
)

func newValidateCmd(machine *bool) *cobra.Command {
	var policyPath string
	cmd := &cobra.Command{
		Use:   "validate <brine.toml> --policy <policy file>",
		Short: "Validate an app against an explicit policy without contacting a host",
		Args:  offlineSpecArg,
		RunE: func(cmd *cobra.Command, args []string) error {
			desired, err := readDesired(cmd.Context(), args[0], policyPath, cmd.ErrOrStderr())
			if err != nil {
				return err
			}
			if *machine {
				keys := make([]string, 0, len(desired.Environment))
				for _, env := range desired.Environment {
					keys = append(keys, env.Name)
				}
				projection := struct {
					policy.Desired
					Environment []string `json:"environment"`
				}{desired, keys}
				return json.NewEncoder(cmd.OutOrStdout()).Encode(result.Success(cmd.CommandPath(), projection))
			}
			return printValidatedApp(cmd.OutOrStdout(), desired)
		},
	}
	cmd.Flags().StringVar(&policyPath, "policy", "", "Explicit operator policy TOML file")
	return cmd
}

func printValidatedApp(output io.Writer, desired policy.Desired) error {
	if desired.SpacetimeDB == nil {
		_, err := fmt.Fprintf(output, "Valid app %s\nImage: %s\nDomains: %s\nContainer port: %d\nPolicy: %s\nEnvironment: %d keys (values hidden), secrets: %d references\n", desired.Name, desired.Image, joinDomains(desired), desired.ContainerPort, desired.PolicyVersion, len(desired.Environment), len(desired.Secrets))
		return err
	}
	db := desired.SpacetimeDB
	if _, err := fmt.Fprintf(output, "Valid app %s\nManaged engine: SpacetimeDB %s\nEngine image: %s\nDatabase: %s\nEngine resources: %d MiB memory, %d PIDs\n", desired.Name, db.Version, db.Image, db.Database, db.Resources.MemoryMB, db.Resources.PIDsLimit); err != nil {
		return err
	}
	frontend := "Frontend: none\n"
	if desired.App().HasAppContainer() {
		frontend = fmt.Sprintf("Frontend image: %s\nFrontend container port: %d\nFrontend resources: %d MiB memory, %d PIDs\n", desired.Image, desired.ContainerPort, desired.Resources.MemoryMB, desired.Resources.PIDsLimit)
	}
	_, err := fmt.Fprintf(output, "%sDomains: %s\nPolicy: %s\nEnvironment: %d keys (values hidden), secrets: %d references\n", frontend, joinDomains(desired), desired.PolicyVersion, len(desired.Environment), len(desired.Secrets))
	return err
}

func newPlanCmd(machine *bool, version string, deps Dependencies) *cobra.Command {
	var offline bool
	var connected operationFlags
	var snapshotPath, policyPath, statePath, outDir string
	cmd := &cobra.Command{
		Use:   "plan <brine.toml> --target NAME | --offline --snapshot <snapshot.json> --policy <policy file>",
		Short: "Plan on an enrolled host or preview a non-applyable offline plan",
		Long:  "Preview a non-applyable offline plan. Use --target NAME for connected planning on an enrolled host. Image platform is an offline assumption from the snapshot, not a registry verification.",
		Args:  offlineSpecArg,
		RunE: func(cmd *cobra.Command, args []string) error {
			if !offline {
				if connected.target == "" {
					return result.New(result.OfflineRequired, nil)
				}
				if snapshotPath != "" || policyPath != "" || statePath != "" || outDir != "" {
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
				request, err := dispatch.NewPlanArgs(string(raw), app)
				if err != nil {
					return result.New(result.InvalidUsage, err)
				}
				response, err := connected.call(cmd.Context(), deps, "plan", request)
				if err != nil {
					return err
				}
				p, ok := response.Data.(dispatch.Planned)
				if !response.OK || !ok {
					return result.New(result.TransportInvalidResponse, nil)
				}
				if *machine {
					return json.NewEncoder(cmd.OutOrStdout()).Encode(result.Success(cmd.CommandPath(), p))
				}
				_, err = fmt.Fprintf(cmd.OutOrStdout(), "Plan %s: %s\n", p.PlanID, p.Kind)
				if err == nil {
					err = printSchemaArtifacts(cmd.OutOrStdout(), p.SchemaArtifacts)
				}
				if err == nil && p.Diff != nil {
					err = json.NewEncoder(cmd.OutOrStdout()).Encode(p.Diff)
				}
				if err == nil && p.Kind == plan.Conflict {
					return printConflicts(cmd.OutOrStdout(), p.Conflicts)
				}
				return err
			}
			if connected.target != "" {
				return result.New(result.InvalidUsage, nil)
			}
			if snapshotPath == "" || (cmd.Flags().Changed("state") && statePath == "") || (cmd.Flags().Changed("out") && outDir == "") {
				return result.New(result.InvalidUsage, nil)
			}
			desired, err := readDesired(cmd.Context(), args[0], policyPath, cmd.ErrOrStderr())
			if err != nil {
				return err
			}
			raw, err := readOfflineInput(snapshotPath, planfile.MaxFileBytes)
			if err != nil {
				return err
			}
			snapshot, err := target.Decode(raw)
			if err != nil {
				return result.New(result.InvalidUsage, err)
			}
			state := plan.BrineState{Releases: []plan.CurrentRelease{}}
			if statePath != "" {
				raw, err := readOfflineInput(statePath, planfile.MaxFileBytes)
				if err != nil {
					return err
				}
				state, err = decodeBrineState(raw)
				if err != nil {
					return result.New(result.InvalidUsage, err)
				}
			} else if snapshot.Generation.Status == target.KnownStatus && *snapshot.Generation.Value == 0 && snapshot.Apps.Status == target.KnownStatus && len(*snapshot.Apps.Value) == 0 {
				// Only an affirmatively empty, generation-zero snapshot can stand in for
				// empty committed state. Installed releases must come from --state.
				state.Target = snapshot.Identity
			}
			image := offlineImage(desired, snapshot)
			input := plan.Input{Desired: desired, Snapshot: snapshot, Image: image, State: state}
			intent, err := plan.Build(input)
			if err != nil {
				return result.New(result.InvalidUsage, err)
			}
			var written string
			if outDir != "" {
				if err := cmd.Context().Err(); err != nil {
					return err
				}
				document, err := planfile.New(input, planfile.Metadata{CreatedAt: time.Now().UTC(), ToolVersion: version})
				if err != nil {
					return err
				}
				written, err = writeOfflinePlan(outDir, document)
				if err != nil {
					return err
				}
			}
			if *machine {
				projection, err := planview.JSON(intent)
				if err != nil {
					return err
				}
				return json.NewEncoder(cmd.OutOrStdout()).Encode(result.Success(cmd.CommandPath(), json.RawMessage(projection)))
			}
			if _, err := fmt.Fprintln(cmd.OutOrStdout(), "OFFLINE PLAN — NOT APPLYABLE"); err != nil {
				return err
			}
			if _, err := fmt.Fprint(cmd.OutOrStdout(), planview.Human(intent, humanTheme(cmd.OutOrStdout()), 80)); err != nil {
				return err
			}
			if _, err := fmt.Fprintln(cmd.OutOrStdout(), "Plan hash:", intent.Hash); err != nil {
				return err
			}
			if written != "" {
				_, err = fmt.Fprintf(cmd.OutOrStdout(), "Plan file: %q\n", written)
			}
			return err
		},
	}
	connected.register(cmd)
	cmd.Flags().BoolVar(&offline, "offline", false, "Require local snapshot planning; never contact infrastructure")
	cmd.Flags().StringVar(&snapshotPath, "snapshot", "", "Target snapshot JSON file")
	cmd.Flags().StringVar(&policyPath, "policy", "", "Explicit operator policy TOML file")
	cmd.Flags().StringVar(&statePath, "state", "", "Committed Brine release state JSON file (required for installed apps)")
	cmd.Flags().StringVar(&outDir, "out", "", "Existing directory for the private, hash-named offline plan file")
	return cmd
}

func offlineImage(desired policy.Desired, snapshot target.Snapshot) plan.Image {
	reference := desired.Image
	if reference == "" && desired.SpacetimeDB != nil {
		reference = desired.SpacetimeDB.Image
	}
	image := plan.Image{ManifestDigest: target.Observation[string]{Status: target.Unknown}, Digest: strings.SplitN(string(reference), "@", 2)[1], Platform: target.Platform{OS: "linux", Arch: snapshot.Arch}}
	if snapshot.Apps.Status != target.KnownStatus {
		return image
	}
	for _, app := range *snapshot.Apps.Value {
		if app.Name == string(desired.Name) && app.Image.Status == target.KnownStatus && app.Image.Value.Digest == image.Digest {
			image.Platform = app.Image.Value.Platform
			return image
		}
	}
	return image
}

func offlineSpecArg(_ *cobra.Command, args []string) error {
	if len(args) != 1 || args[0] == "" {
		return result.New(result.InvalidUsage, nil)
	}
	return nil
}

func readDesired(ctx context.Context, specPath, policyPath string, diagnostics io.Writer) (policy.Desired, error) {
	if policyPath == "" {
		return policy.Desired{}, result.New(result.InvalidUsage, nil)
	}
	raw, err := readOfflineInput(specPath, 1<<20)
	if err != nil {
		return policy.Desired{}, err
	}
	app, err := parseClientSpec(ctx, specPath, raw, diagnostics)
	if err != nil {
		return policy.Desired{}, result.New(result.InvalidUsage, err)
	}
	raw, err = readOfflineInput(policyPath, 1<<20)
	if err != nil {
		return policy.Desired{}, err
	}
	pol, err := policy.Parse(raw)
	if err != nil {
		return policy.Desired{}, result.New(result.PolicyRefused, err)
	}
	desired, err := policy.Normalize(app, pol)
	if err != nil {
		return policy.Desired{}, result.New(result.PolicyRefused, err)
	}
	return desired, nil
}

func readOfflineInput(path string, limit int64) ([]byte, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, result.New(result.InvalidUsage, err)
	}
	if !info.Mode().IsRegular() || info.Size() > limit {
		return nil, result.New(result.InvalidUsage, nil)
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, result.New(result.InvalidUsage, err)
	}
	defer file.Close()
	info, err = file.Stat()
	if err != nil {
		return nil, result.New(result.InvalidUsage, err)
	}
	if !info.Mode().IsRegular() || info.Size() > limit {
		return nil, result.New(result.InvalidUsage, nil)
	}
	raw, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil {
		return nil, result.New(result.InvalidUsage, err)
	}
	if int64(len(raw)) > limit {
		return nil, result.New(result.InvalidUsage, nil)
	}
	return raw, nil
}

func writeOfflinePlan(dir string, document planfile.Offline) (string, error) {
	path := filepath.Join(dir, document.Filename())
	if info, err := os.Lstat(path); err == nil {
		if !info.Mode().IsRegular() || info.Mode().Perm() != 0600 {
			return "", result.New(result.Conflict, nil)
		}
		// Reuse verified immutable evidence on a retry, including its original
		// creation metadata. A fresh timestamp must not turn a retry into a rewrite.
		existing, err := planfile.Read(path)
		if err != nil || existing.Hash() != document.Hash() {
			return "", result.New(result.Conflict, err)
		}
		return path, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	written, err := planfile.Write(dir, document)
	if err != nil {
		var conflict *planfile.ConflictError
		if errors.As(err, &conflict) {
			return "", result.New(result.Conflict, err)
		}
	}
	return written, err
}

func joinDomains(desired policy.Desired) string {
	domains := make([]string, len(desired.Domains))
	for i, d := range desired.Domains {
		domains[i] = string(d)
	}
	return strings.Join(domains, ", ")
}
