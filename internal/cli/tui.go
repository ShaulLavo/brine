package cli

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"slices"
	"strings"

	"github.com/ShaulLavo/brine/internal/result"
	"github.com/ShaulLavo/brine/internal/transport"
	"github.com/ShaulLavo/brine/internal/ui"
	"github.com/spf13/cobra"
)

func newTUICmd(machineOutput, noInput *bool, deps Dependencies) *cobra.Command {
	var flags operationFlags
	cmd := &cobra.Command{
		Use:   "tui",
		Short: "Browse targets, apps and bounded logs in a read-only terminal UI",
		Long:  "Browse enrolled targets, app status and bounded, redacted log tails. The TUI only reads: it never plans, applies or cancels anything. Without --target it starts on the target picker.",
		RunE: func(cmd *cobra.Command, args []string) error {
			if *machineOutput || *noInput {
				return result.New(result.TUIInteractive, nil)
			}
			if !validTUISelection(flags, cmd.Flags().Changed("target")) {
				return result.New(result.InvalidUsage, nil)
			}
			if !isTerminal(deps.Stdout) || (!isTerminal(deps.Stdin) && !hasProcessTerminalFallback(deps.Stdin)) {
				return result.New(result.TUITerminalRequired, nil)
			}
			if flags.target != "" {
				if _, err := flags.loadTarget(deps); err != nil {
					return err
				}
			}
			ctx, cancel := context.WithCancel(cmd.Context())
			defer cancel()
			_, err := deps.RunTUI(ctx, newTUIModel(ctx, deps, flags), cmd.InOrStdin(), deps.Stdout)
			// Cancel in-flight reads before the error is presented.
			cancel()
			return err
		},
	}
	flags.register(cmd)
	return cmd
}

// validTUISelection checks the flags before the terminal check and before any
// target I/O. An explicit --target must name a valid target.
func validTUISelection(flags operationFlags, targetSet bool) bool {
	if targetSet {
		return flags.validSelection()
	}
	return ui.Sanitize(flags.configDir) == flags.configDir
}

// listTargets returns enrolled target names from file names alone, sorted. It
// never opens a target file, so the picker cannot display its destination,
// identity or host key. A missing directory has no targets.
func listTargets(flags operationFlags) ([]string, error) {
	dir, err := flags.targetsDir()
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return []string{}, nil
	}
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		name, json := strings.CutSuffix(entry.Name(), ".json")
		if json && !entry.IsDir() && transport.ValidTargetName(name) {
			names = append(names, name)
		}
	}
	// Sort by name, not file name: "fixture-pi.json" sorts before "fixture.json".
	slices.Sort(names)
	return names, nil
}
