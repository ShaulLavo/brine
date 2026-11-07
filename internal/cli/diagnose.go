package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"slices"
	"strings"

	"github.com/ShaulLavo/brine/internal/diagnose"
	"github.com/ShaulLavo/brine/internal/result"
	"github.com/spf13/cobra"
)

func newDiagnoseCmd(deps Dependencies, modes *machineModes) *cobra.Command {
	var flags operationFlags
	cmd := &cobra.Command{Use: "diagnose [APP] --target NAME", Short: "Gather a bounded read-only troubleshooting report", Args: cobra.MaximumNArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		request := diagnose.Request{}
		if len(args) > 0 {
			request.App = args[0]
		}
		report, err := readDiagnosis(cmd.Context(), deps, flags, request)
		if err != nil {
			return err
		}
		encoder := json.NewEncoder(cmd.OutOrStdout())
		if modes.json {
			return encoder.Encode(result.Success(cmd.CommandPath(), report))
		}
		if modes.jsonl {
			return encoder.Encode(result.Success(cmd.CommandPath(), struct {
				Event  string          `json:"event"`
				Report diagnose.Report `json:"report"`
			}{"complete", report}))
		}
		return printDiagnosis(cmd.OutOrStdout(), report, flags)
	}}
	flags.register(cmd)
	return cmd
}
func readDiagnosis(ctx context.Context, deps Dependencies, flags operationFlags, request diagnose.Request) (diagnose.Report, error) {
	if err := request.Validate(); err != nil {
		return diagnose.Report{}, err
	}
	response, err := flags.call(ctx, deps, "diagnose", request)
	if err != nil {
		return diagnose.Report{}, err
	}
	if !response.OK {
		return diagnose.Report{}, rejectedDiagnosis(response)
	}
	raw, err := json.Marshal(response.Data)
	if err != nil {
		return diagnose.Report{}, result.New(result.TransportInvalidResponse, nil)
	}
	report, err := diagnose.DecodeReport(raw)
	if err != nil {
		return diagnose.Report{}, err
	}
	if request.App != "" && (len(report.Apps) != 1 || report.Apps[0].Name != request.App) {
		return diagnose.Report{}, result.New(result.TransportInvalidResponse, nil)
	}
	return report, nil
}

func rejectedDiagnosis(response result.Envelope) error {
	if response.Error != nil {
		return result.New(response.Error.Code, nil)
	}
	return result.New(result.TransportInvalidResponse, nil)
}

func printDiagnosis(out io.Writer, report diagnose.Report, flags operationFlags) error {
	lines, err := diagnosisLines(report, flags)
	if err != nil {
		return err
	}
	_, err = io.WriteString(out, strings.Join(lines, "\n")+"\n")
	return err
}

func diagnosisLines(report diagnose.Report, flags operationFlags) ([]string, error) {
	// Render every fact, including unknown reasons, rather than presenting missing data as healthy.
	raw, err := json.Marshal(report)
	if err != nil {
		return nil, err
	}
	var tree map[string]any
	if err = json.Unmarshal(raw, &tree); err != nil {
		return nil, err
	}
	var text strings.Builder
	fmt.Fprintf(&text, "Diagnosis for target %s\n", flags.target)
	printFacts(&text, "host", tree["host"])
	printFacts(&text, "app_names", tree["app_names"])
	for i, app := range report.Apps {
		fmt.Fprintf(&text, "\nApp %s\n", app.Name)
		printFacts(&text, "", tree["apps"].([]any)[i])
	}
	if report.Truncated {
		text.WriteString("\nReport truncated to the first 16 apps. Diagnose an app by name for more detail.\n")
	}
	text.WriteString("\nFindings\n")
	if len(report.Findings) == 0 {
		text.WriteString("No failure rule matched. Unknown facts are not evidence of health.\n")
	}
	for _, finding := range report.Findings {
		scope := "host"
		if finding.App != "" {
			scope = finding.App
		}
		fmt.Fprintf(&text, "[%s] %s (%s): %s\n", finding.Severity, finding.Code, scope, finding.Message)
		for _, next := range finding.NextOperations {
			fmt.Fprintf(&text, "  Next: %s\n", diagnosisCommand(next, flags))
		}
	}
	return strings.Split(strings.TrimSuffix(text.String(), "\n"), "\n"), nil
}

func diagnosisCommand(next string, flags operationFlags) string {
	args := strings.Fields(next)
	for i, arg := range args {
		if arg == "--target" && i+1 < len(args) && args[i+1] == "NAME" {
			args = append(args[:i], args[i+2:]...)
			return flags.command(args[1:]...)
		}
	}
	return next
}

func printFacts(out *strings.Builder, path string, value any) {
	object, ok := value.(map[string]any)
	if !ok {
		raw, _ := json.Marshal(value)
		fmt.Fprintf(out, "  %s: %s\n", path, raw)
		return
	}
	if status, ok := object["status"].(string); ok {
		if status == "known" {
			raw, _ := json.Marshal(object["value"])
			fmt.Fprintf(out, "  %s: %s\n", path, raw)
		} else {
			fmt.Fprintf(out, "  %s: %s (%s)\n", path, status, object["reason"])
		}
		return
	}
	keys := make([]string, 0, len(object))
	for key := range object {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	for _, key := range keys {
		child := key
		if path != "" {
			child = path + "." + key
		}
		printFacts(out, child, object[key])
	}
}
