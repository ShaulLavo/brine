package cli

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/ShaulLavo/brine/internal/dispatch"
	"github.com/ShaulLavo/brine/internal/plan"
	"github.com/ShaulLavo/brine/internal/result"
	"github.com/ShaulLavo/brine/internal/target"
	"github.com/ShaulLavo/brine/internal/transport"
)

type listenerChurnDeployHost struct {
	*firstDeployHost
	input   plan.Input
	inRange bool
	samples int
}

func (h *listenerChurnDeployHost) Call(ctx context.Context, remote transport.Target, request dispatch.Request) (result.Envelope, error) {
	response, err := h.firstDeployHost.Call(ctx, remote, request)
	if err != nil || request.Op != "deploy_state" && request.Op != "deploy_checkpoint" {
		return response, err
	}
	h.samples++
	ports := []target.Port{45006, 55650}
	port := ports[0]
	if h.prepared {
		port = ports[h.samples%len(ports)]
	}
	h.input.Snapshot.UsedPorts = target.Known([]target.Port{port})
	if h.inRange && h.prepared {
		h.input.Snapshot.UsedPorts = target.Known([]target.Port{20000})
	}
	review, err := plan.DeploymentReviewHash(h.input)
	if err != nil {
		return result.Envelope{}, err
	}
	switch value := response.Data.(type) {
	case dispatch.DeploymentState:
		value.ReviewHash = review
		response.Data = value
	case dispatch.DeploymentCheckpoint:
		value.ReviewHash = review
		response.Data = value
	}
	return response, nil
}

func TestCombinedDeployListenerChurnBetweenSteps(t *testing.T) {
	for _, inRange := range []bool{false, true} {
		t.Run(map[bool]string{false: "ephemeral UDP churn", true: "new in-range listener"}[inRange], func(t *testing.T) {
			host, deps, args, _, stderr := deploymentFixture(t)
			raw, err := os.ReadFile(offlineState(t))
			if err != nil {
				t.Fatal(err)
			}
			var state plan.BrineState
			if err = json.Unmarshal(raw, &state); err != nil {
				t.Fatal(err)
			}
			raw, err = os.ReadFile(offlineExamples + "fresh-host.json")
			if err != nil {
				t.Fatal(err)
			}
			snapshot, err := target.Decode(raw)
			if err != nil {
				t.Fatal(err)
			}
			release := state.Releases[0]
			state = plan.BrineState{Target: snapshot.Identity, Generation: *snapshot.Generation.Value, Releases: []plan.CurrentRelease{}}
			client := &listenerChurnDeployHost{firstDeployHost: host, input: plan.Input{Desired: release.Desired, Image: release.Image, Snapshot: snapshot, State: state}, inRange: inRange}
			deps.OperationClient = client
			err = Execute(deps, args)
			if inRange {
				if result.ExitCode(err) != 5 || strings.Join(host.submitted, ",") != "preparation" {
					t.Fatal("listener drift did not stop before credentials", err, host.submitted)
				}
			} else if err != nil || strings.Join(host.submitted, ",") != "preparation,credentials,initialization,release" || client.samples < 4 {
				t.Fatal("UDP churn interrupted combined deploy", err, host.submitted, stderr.String())
			}
		})
	}
}
