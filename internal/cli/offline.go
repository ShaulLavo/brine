package cli

import (
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
	"github.com/ShaulLavo/brine/internal/spec"
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
			desired, err := readDesired(args[0], policyPath)
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
			_, err = fmt.Fprintf(cmd.OutOrStdout(), "Valid app %s\nImage: %s\nDomains: %s\nContainer port: %d\nPolicy: %s\nEnvironment: %d keys (values hidden), secrets: %d references\n", desired.Name, desired.Image, joinDomains(desired), desired.ContainerPort, desired.PolicyVersion, len(desired.Environment), len(desired.Secrets))
			return err
		},
	}
	cmd.Flags().StringVar(&policyPath, "policy", "", "Explicit operator policy TOML file")
	return cmd
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
				if _, err = spec.Parse(raw); err != nil {
					return err
				}
				response, err := connected.call(cmd.Context(), deps, "plan", dispatch.PlanArgs{Spec: string(raw)})
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
				if err == nil && p.Diff != nil {
					err = json.NewEncoder(cmd.OutOrStdout()).Encode(p.Diff)
				}
				return err
			}
			if connected.target != "" {
				return result.New(result.InvalidUsage, nil)
			}
			if snapshotPath == "" || (cmd.Flags().Changed("state") && statePath == "") || (cmd.Flags().Changed("out") && outDir == "") {
				return result.New(result.InvalidUsage, nil)
			}
			desired, err := readDesired(args[0], policyPath)
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
			image := plan.Image{ManifestDigest: target.Observation[string]{Status: target.Unknown}, Digest: strings.SplitN(string(desired.Image), "@", 2)[1], Platform: target.Platform{OS: "linux", Arch: snapshot.Arch}}
			if snapshot.Apps.Status == target.KnownStatus {
				for _, app := range *snapshot.Apps.Value {
					if app.Name == string(desired.Name) && app.Image.Status == target.KnownStatus && app.Image.Value.Digest == image.Digest {
						image.Platform = app.Image.Value.Platform
						break
					}
				}
			}
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

func offlineSpecArg(_ *cobra.Command, args []string) error {
	if len(args) != 1 || args[0] == "" {
		return result.New(result.InvalidUsage, nil)
	}
	return nil
}

func readDesired(specPath, policyPath string) (policy.Desired, error) {
	if policyPath == "" {
		return policy.Desired{}, result.New(result.InvalidUsage, nil)
	}
	raw, err := readOfflineInput(specPath, 1<<20)
	if err != nil {
		return policy.Desired{}, err
	}
	app, err := spec.Parse(raw)
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
