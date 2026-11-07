package cli

import (
	"github.com/ShaulLavo/brine/internal/enroll"
	"github.com/spf13/cobra"
)

func newHostAdmissionSettleCmd() *cobra.Command {
	return &cobra.Command{Use: "admission-settle", Hidden: true, Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error { return enroll.AdmissionSettle(cmd.Context()) }}
}
