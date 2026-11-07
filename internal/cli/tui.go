package cli

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"slices"
	"strings"

	"github.com/ShaulLavo/brine/internal/jobs"
	"github.com/ShaulLavo/brine/internal/result"
	"github.com/ShaulLavo/brine/internal/transport"
	"github.com/ShaulLavo/brine/internal/ui"
	"github.com/spf13/cobra"
)

func newTUICmd(machineOutput, noInput *bool, deps Dependencies) *cobra.Command {
	var flags operationFlags
	var entry tuiEntry
	cmd := &cobra.Command{
		Use:   "tui",
		Short: "Browse operations and review stateless HTTP releases or lifecycle plans",
		Long:  "Browse enrolled targets, app status, operation timelines and bounded, redacted log tails. Use --spec FILE to plan a stateless HTTP release, or --operation ID to resume observation without a committed app. Database, managed-engine and UDP specs use deploy instead. Review release, restart, stop, start or previous known-good rollback plans, then confirm with a and y to apply. Uncertain acceptance retains the same plan and key for explicit recovery. Quitting never cancels a host job. Without --target it starts on the target picker.",
		RunE: func(cmd *cobra.Command, args []string) error {
			if *machineOutput || *noInput {
				return result.New(result.TUIInteractive, nil)
			}
			if len(args) != 0 || !validTUIEntry(entry, cmd.Flags().Changed("spec"), cmd.Flags().Changed("operation")) || !validTUISelection(flags, cmd.Flags().Changed("target")) {
				return result.New(result.InvalidUsage, nil)
			}
			if !isTerminal(deps.Stdout) || (!isTerminal(deps.Stdin) && !hasProcessTerminalFallback(deps.Stdin)) {
				return result.New(result.TUITerminalRequired, nil)
			}
			return runTUIEntry(cmd, deps, flags, entry)
		},
	}
	flags.register(cmd)
	cmd.Flags().StringVar(&entry.Spec, "spec", "", "Local stateless HTTP app spec to plan and review")
	cmd.Flags().StringVar(&entry.Operation, "operation", "", "Existing operation ID to observe, even without a committed app")
	return cmd
}

func runTUIEntry(cmd *cobra.Command, deps Dependencies, flags operationFlags, entry tuiEntry) error {
	if flags.target != "" && entry.Spec == "" {
		if _, err := flags.loadTarget(deps); err != nil {
			return err
		}
	}
	ctx, cancel := context.WithCancel(cmd.Context())
	defer cancel()
	final, err := deps.RunTUI(ctx, newTUIModel(ctx, deps, flags, entry), cmd.InOrStdin(), deps.Stdout)
	// Cancel in-flight reads before the error is presented.
	cancel()
	if noticeErr := writeTUIExitNotice(final, deps.Stderr); noticeErr != nil {
		return errors.Join(err, noticeErr)
	}
	return err
}

func validTUIEntry(entry tuiEntry, specSet, operationSet bool) bool {
	if specSet && operationSet {
		return false
	}
	if specSet {
		return entry.Spec != "" && ui.Sanitize(entry.Spec) == entry.Spec
	}
	return !operationSet || jobs.ValidID(entry.Operation)
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
