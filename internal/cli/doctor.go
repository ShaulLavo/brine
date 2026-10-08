package cli

import (
	"encoding/json"
	"fmt"
	"runtime"

	"github.com/charmbracelet/lipgloss"
	"github.com/spf13/cobra"
)

type toolCheck struct {
	Name      string `json:"name"`
	Available bool   `json:"available"`
	Path      string `json:"path,omitempty"`
}

func checkTool(name string, lookPath func(string) (string, error)) toolCheck {
	path, err := lookPath(name)
	return toolCheck{Name: name, Available: err == nil, Path: path}
}

func newDoctorCmd(jsonOutput *bool, lookPath func(string) (string, error)) *cobra.Command {
	return &cobra.Command{
		Use:   "doctor",
		Short: "Check local deployment tooling (no server changes)",
		RunE: func(cmd *cobra.Command, args []string) error {
			names := []string{"podman", "systemctl", "caddy", "litestream", "tailscale"}
			checks := make([]toolCheck, 0, len(names))
			for _, name := range names {
				checks = append(checks, checkTool(name, lookPath))
			}

			if *jsonOutput {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(map[string]any{
					"schema_version": 1,
					"platform":       runtime.GOOS,
					"checks":         checks,
				})
			}
			head := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("12"))
			fmt.Fprintln(cmd.OutOrStdout(), head.Render("Deployment tool check"))
			fmt.Fprintln(cmd.OutOrStdout(), "Checking PATH only; this is not a server readiness audit.")
			for _, c := range checks {
				status := "missing"
				if c.Available {
					status = "found"
				}
				fmt.Fprintf(cmd.OutOrStdout(), "  %-12s %s\n", c.Name, status)
			}
			return nil
		},
	}
}
