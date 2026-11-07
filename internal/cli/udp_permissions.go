package cli

import (
	"encoding/json"

	"github.com/ShaulLavo/brine/internal/result"
	"github.com/spf13/cobra"
)

func newHostUDPGrantCheckCmd() *cobra.Command {
	return &cobra.Command{Use: "udp-grant-check APP", Hidden: true, Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		ports, _ := cmd.Context().Value(hostControlKey{}).(hostControlPorts)
		if ports.udpHeld == nil {
			return result.New(result.DependencyMissing, nil)
		}
		held, err := ports.udpHeld(cmd.Context(), args[0])
		if err != nil {
			return err
		}
		return json.NewEncoder(cmd.OutOrStdout()).Encode(struct {
			Held bool `json:"held"`
		}{held})
	}}
}
