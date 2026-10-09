package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/ShaulLavo/brine/internal/enroll"
	"github.com/ShaulLavo/brine/internal/result"
	"github.com/charmbracelet/x/term"
	"github.com/spf13/cobra"
)

func newEnrollCmd(deps Dependencies, noInput *bool, modes *machineModes) *cobra.Command {
	var o enroll.Options

	cmd := &cobra.Command{Use: "enroll <ssh-destination>", Short: "Enroll through the operator's own admin SSH access", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		if modes.enabled() {
			return result.New(result.InvalidUsage, nil)
		}
		o.Destination = args[0]
		if o.Name == "" {
			o.Name = enroll.DefaultTargetName(args[0])
		}
		if o.ConfigDir == "" {
			dir, err := os.UserConfigDir()
			if err != nil {
				return err
			}
			o.ConfigDir = filepath.Join(dir, "brine", "targets")
		}
		o.NoInput = *noInput
		terminal := false
		if f, ok := deps.Stdin.(*os.File); ok {
			terminal = term.IsTerminal(f.Fd())
		}
		// --yes never bypasses the target-name confirmation.
		err := (enroll.Client{Input: deps.Stdin, Output: deps.Stdout, Terminal: terminal}).Run(cmd.Context(), o)
		if err != nil {
			fmt.Fprintln(deps.Stderr, err)
		}
		return err
	}}
	cmd.Flags().StringVar(&o.Name, "target-name", "", "Name of the pinned client target")
	cmd.Flags().StringVar(&o.DeployKeyPath, "deploy-key", "", "Path to the operator-supplied Ed25519 public deploy key")
	cmd.Flags().StringVar(&o.IdentityPath, "identity", "", "Private key path (defaults to --deploy-key without .pub)")
	cmd.Flags().StringVar(&o.HostBinary, "host-binary", "", "Matching-architecture Linux brine binary")
	cmd.Flags().StringVar(&o.ConfigDir, "config-dir", "", "Private client target directory")
	cmd.Flags().BoolVar(&o.Undo, "undo", false, "Remove only verified enrollment-owned resources")
	cmd.Flags().Bool("yes", false, "Still requires terminal stdin and typed target-name confirmation")
	return cmd
}
func newHostEnrollmentCmd(deps Dependencies) *cobra.Command {
	return &cobra.Command{Use: "enrollment", Hidden: true, Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		data, err := io.ReadAll(io.LimitReader(deps.Stdin, 16<<10+1))
		if err != nil || len(data) > 16<<10 {
			return result.New(result.InvalidUsage, err)
		}
		var request enroll.HostRequest
		if err = json.Unmarshal(data, &request); err != nil {
			return result.New(result.InvalidUsage, err)
		}
		value, err := enroll.HostOperation(cmd.Context(), request)
		if err != nil {
			fmt.Fprintln(deps.Stderr, err)
			return err
		}
		return json.NewEncoder(deps.Stdout).Encode(value)
	}}
}
