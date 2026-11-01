package cli

import (
	"encoding/json"

	"github.com/ShaulLavo/brine/internal/enroll"
	"github.com/spf13/cobra"
)

func newHostUDPGrantCheckCmd() *cobra.Command {
	return &cobra.Command{Use: "udp-grant-check APP", Hidden: true, Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		held, err := enroll.UDPGrantHeld(cmd.Context(), args[0])
		if err != nil {
			return err
		}
		return json.NewEncoder(cmd.OutOrStdout()).Encode(struct {
			Held bool `json:"held"`
		}{held})
	}}
}
