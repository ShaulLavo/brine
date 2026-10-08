package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"runtime"
	"time"

	"charm.land/lipgloss/v2"
	"github.com/ShaulLavo/brine/internal/localexec"
	"github.com/ShaulLavo/brine/internal/result"
	"github.com/ShaulLavo/brine/internal/ui"
	"github.com/spf13/cobra"
)

const doctorTimeout = 2 * time.Second

type toolCheck struct {
	Name      string `json:"name"`
	Required  bool   `json:"required"`
	Available bool   `json:"available"`
	Version   string `json:"version"`
	Reason    string `json:"reason"`
}

type localTool struct {
	name     string
	required bool
	args     []string
	version  *regexp.Regexp
}

var clientTools = []localTool{
	{"ssh", true, []string{"-V"}, regexp.MustCompile(`(?m)^OpenSSH_(?:for_Windows_)?([0-9]+\.[0-9]+(?:p[0-9]+)?)(?:[ ,\r\n]|$)`)},
	{"git", false, []string{"--version"}, regexp.MustCompile(`(?m)^git version ([0-9]+\.[0-9]+(?:\.[0-9]+)*(?:\.windows\.[0-9]+)?)(?:[ \r\n]|$)`)},
}

func checkTool(ctx context.Context, tool localTool, lookPath func(string) (string, error), runner localexec.Runner) toolCheck {
	check := toolCheck{Name: tool.name, Required: tool.required}
	path, err := lookPath(tool.name)
	if err != nil {
		check.Reason = "not_found"
		return check
	}
	check.Available = true
	probeCtx, cancel := context.WithTimeout(ctx, doctorTimeout)
	defer cancel()
	output, err := runner.Run(probeCtx, path, tool.args...)
	switch {
	case errors.Is(probeCtx.Err(), context.DeadlineExceeded), errors.Is(err, context.DeadlineExceeded):
		check.Reason = "timeout"
	case err != nil:
		check.Reason = "exit_error"
	default:
		if len(output) > localexec.OutputLimit {
			output = output[:localexec.OutputLimit]
		}
		match := tool.version.FindStringSubmatch(output)
		if len(match) != 2 {
			check.Reason = "unparseable"
		} else {
			check.Version = match[1]
		}
	}
	return check
}

func newDoctorCmd(jsonOutput *bool, lookPath func(string) (string, error), runner localexec.Runner) *cobra.Command {
	if runner == nil {
		runner = localexec.ExecRunner{}
	}
	return &cobra.Command{
		Use:   "doctor",
		Short: "Check local client dependencies (not remote host readiness)",
		RunE: func(cmd *cobra.Command, args []string) error {
			checks := make([]toolCheck, 0, len(clientTools))
			missingRequired := false
			for _, tool := range clientTools {
				checks = append(checks, checkTool(cmd.Context(), tool, lookPath, runner))
				if err := cmd.Context().Err(); err != nil {
					return err
				}
				check := checks[len(checks)-1]
				missingRequired = missingRequired || (check.Required && check.Reason != "")
			}
			var dependencyErr error
			if missingRequired {
				dependencyErr = result.New(result.DependencyMissing, nil)
			}
			if *jsonOutput {
				if dependencyErr != nil {
					return dependencyErr
				}
				return json.NewEncoder(cmd.OutOrStdout()).Encode(result.Success(cmd.CommandPath(), map[string]any{
					"scope":                 "local_client",
					"remote_host_readiness": false,
					"platform":              runtime.GOOS,
					"checks":                checks,
				}))
			}
			theme := ui.ThemeFromEnv()
			if _, err := lipgloss.Fprintln(cmd.OutOrStdout(), theme.Title.Render("Local client dependency check")); err != nil {
				return err
			}
			if _, err := lipgloss.Fprintln(cmd.OutOrStdout(), theme.Help.Render("Checks this machine only; not a remote host readiness audit. No changes are made.")); err != nil {
				return err
			}
			for _, check := range checks {
				requirement := "optional"
				if check.Required {
					requirement = "required"
				}
				status := check.Version
				if check.Reason != "" {
					status = check.Reason
				}
				if _, err := fmt.Fprintf(cmd.OutOrStdout(), "  %-12s %-8s %s\n", check.Name, requirement, status); err != nil {
					return err
				}
			}
			return dependencyErr
		},
	}
}
