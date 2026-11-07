package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/ShaulLavo/brine/internal/dispatch"
	"github.com/ShaulLavo/brine/internal/readiness"
	"io"
	"regexp"
	"runtime"
	"strings"
	"time"

	"charm.land/lipgloss/v2"
	"github.com/ShaulLavo/brine/internal/localexec"
	"github.com/ShaulLavo/brine/internal/result"
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

type localDoctorReport struct {
	Checks              []toolCheck `json:"checks"`
	Platform            string      `json:"platform"`
	RemoteHostReadiness bool        `json:"remote_host_readiness"`
	Scope               string      `json:"scope"`
}
type remoteDoctorReport struct {
	Client localDoctorReport `json:"client"`
	Host   readiness.Report  `json:"host"`
}

func newDoctorCmd(deps Dependencies, modes *machineModes) *cobra.Command {
	var flags operationFlags
	runner := deps.DoctorRunner
	if runner == nil {
		runner = localexec.ExecRunner{}
	}
	cmd := &cobra.Command{Use: "doctor [--target NAME]", Short: "Check client dependencies and optional read-only target readiness", RunE: func(cmd *cobra.Command, _ []string) error {
		if cmd.Flags().Changed("target") && !flags.validSelection() {
			return result.New(result.InvalidUsage, nil)
		}
		client, dependencyErr := collectDoctorClient(cmd.Context(), deps.LookPath, runner)
		if err := cmd.Context().Err(); err != nil {
			return err
		}
		if modes.enabled() && dependencyErr != nil {
			return dependencyErr
		}
		if flags.target == "" {
			if modes.enabled() {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(result.Success(cmd.CommandPath(), client))
			}
			if err := printDoctorClient(cmd, client); err != nil {
				return err
			}
			return dependencyErr
		}
		if dependencyErr != nil {
			return dependencyErr
		}
		host, err := doctorTarget(cmd, deps, flags)
		if err != nil {
			return err
		}
		report := remoteDoctorReport{Client: client, Host: host}
		if modes.enabled() {
			return writeDoctor(cmd, modes, report)
		}
		if err := printDoctorClient(cmd, client); err != nil {
			return err
		}
		return printDoctorTarget(cmd, flags, host)
	}}
	flags.register(cmd)
	return cmd
}
func collectDoctorClient(ctx context.Context, lookPath func(string) (string, error), runner localexec.Runner) (localDoctorReport, error) {
	client := localDoctorReport{Scope: "local_client", RemoteHostReadiness: false, Platform: runtime.GOOS, Checks: []toolCheck{}}
	var missing error
	for _, tool := range clientTools {
		check := checkTool(ctx, tool, lookPath, runner)
		client.Checks = append(client.Checks, check)
		if check.Required && check.Reason != "" {
			missing = result.New(result.DependencyMissing, nil)
		}
		if ctx.Err() != nil {
			return client, ctx.Err()
		}
	}
	return client, missing
}
func printDoctorClient(cmd *cobra.Command, client localDoctorReport) error {
	theme := humanTheme(cmd.OutOrStdout())
	if _, err := lipgloss.Fprintln(cmd.OutOrStdout(), theme.Title.Render("Local client dependency check")); err != nil {
		return err
	}
	if _, err := lipgloss.Fprintln(cmd.OutOrStdout(), theme.Help.Render("Checks this machine only; not a remote host readiness audit. No changes are made.")); err != nil {
		return err
	}
	for _, check := range client.Checks {
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
	return nil
}
func doctorTarget(cmd *cobra.Command, deps Dependencies, flags operationFlags) (readiness.Report, error) {
	response, err := flags.call(cmd.Context(), deps, "readiness", dispatch.ReadinessArgs{})
	if err != nil {
		return readiness.Report{}, err
	}
	if !response.OK {
		if response.Error != nil {
			return readiness.Report{}, result.New(response.Error.Code, nil)
		}
		return readiness.Report{}, result.New(result.TransportInvalidResponse, nil)
	}
	raw, err := json.Marshal(response.Data)
	if err != nil {
		return readiness.Report{}, result.New(result.TransportInvalidResponse, nil)
	}
	return readiness.DecodeReport(raw)
}
func writeDoctor(cmd *cobra.Command, modes *machineModes, report any) error {
	if modes.jsonl {
		report = struct {
			Event  string `json:"event"`
			Report any    `json:"report"`
		}{"complete", report}
	}
	return json.NewEncoder(cmd.OutOrStdout()).Encode(result.Success(cmd.CommandPath(), report))
}
func printDoctorTarget(cmd *cobra.Command, flags operationFlags, report readiness.Report) error {
	raw, err := json.Marshal(report)
	if err != nil {
		return err
	}
	var tree map[string]any
	if err := json.Unmarshal(raw, &tree); err != nil {
		return err
	}
	var text strings.Builder
	fmt.Fprintf(&text, "\nRead-only readiness for target %s\n", flags.target)
	text.WriteString("A collected report is not host health or approval. Unknown facts are not evidence of readiness.\n")
	printFacts(&text, "host", tree)
	_, err = io.WriteString(cmd.OutOrStdout(), text.String())
	return err
}
