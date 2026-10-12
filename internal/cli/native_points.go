package cli

import (
	"encoding/json"
	"fmt"

	"github.com/ShaulLavo/brine/internal/dispatch"
	"github.com/ShaulLavo/brine/internal/nativecapture"
	"github.com/ShaulLavo/brine/internal/result"
	"github.com/ShaulLavo/brine/internal/transport"
	"github.com/spf13/cobra"
)

func newNativePointCmd(deps Dependencies, modes *machineModes) *cobra.Command {
	var flags operationFlags
	kind := "verified"
	cmd := &cobra.Command{Use: "point POINT_ID --target NAME", Short: "Inspect a retained native point reference", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		input := dispatch.NativePointArgs{Kind: kind, PointID: args[0]}
		if !input.Valid() || !transport.ValidTargetName(flags.target) {
			return result.New(result.InvalidUsage, nil)
		}
		response, err := flags.call(cmd.Context(), deps, "native_point", input)
		if err != nil {
			return err
		}
		point, ok := response.Data.(nativecapture.PointInspection)
		if !response.OK || !ok || !point.Valid() || point.PointID != input.PointID || point.Kind != input.Kind {
			return result.New(result.TransportInvalidResponse, nil)
		}
		if modes.enabled() {
			return json.NewEncoder(cmd.OutOrStdout()).Encode(result.Success(cmd.CommandPath(), point))
		}
		_, err = fmt.Fprintf(cmd.OutOrStdout(), "%s native point %s in epoch %s. Native backup and data effects are unavailable.\n", point.Kind, point.PointID, point.EpochID)
		return err
	}}
	cmd.Flags().StringVar(&kind, "kind", "verified", "Point reference kind: verified or prepared")
	flags.register(cmd)
	return cmd
}
