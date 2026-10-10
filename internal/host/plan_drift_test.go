package host

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/ShaulLavo/brine/internal/dispatch"
	"github.com/ShaulLavo/brine/internal/jobs"
	"github.com/ShaulLavo/brine/internal/ops"
	"github.com/ShaulLavo/brine/internal/plan"
	"github.com/ShaulLavo/brine/internal/target"
)

func TestDetachedStalePlanReportsDecisionPathsBeforeEffects(t *testing.T) {
	r := newDeployRig(t)
	r.inventory.snapshot.UsedPorts = target.Known([]target.Port{25000})
	planned := r.call(t, "plan", dispatch.PlanArgs{Spec: r.spec}).Data.(dispatch.Planned)
	accepted := r.call(t, "apply", dispatch.ApplyArgs{PlanID: planned.PlanID, IdempotencyKey: "drift-diagnostic"}).Data.(jobs.Accepted)
	r.inventory.snapshot.Identity.ID = "private-identity"
	r.inventory.snapshot.UsedPorts = target.Known([]target.Port{25001})
	r.inventory.snapshot.Versions.Systemd = target.Known("257.1")
	if err := r.runner.Run(context.Background(), accepted.OperationID); err == nil {
		t.Fatal("stale plan accepted")
	}
	status := r.call(t, "operation", dispatch.OperationArgs{OperationID: accepted.OperationID}).Data.(jobs.Status)
	if status.Operation.State != ops.Failed || r.pulls != 0 || r.units.installs != 0 {
		t.Fatal("refusal had effects", status.Operation.State)
	}
	var found, failed bool
	for _, event := range status.Events {
		if event.Kind == "plan_drift" {
			var drift plan.DecisionDrift
			if json.Unmarshal(event.Payload, &drift) != nil || !drift.Valid() || !slices.Contains(drift.Paths, "snapshot.identity.id") || !slices.Contains(drift.Paths, "snapshot.used_ports.value[0]") || !slices.Contains(drift.Paths, "snapshot.versions.systemd.value") {
				t.Fatal("missing changed paths", string(event.Payload))
			}
			if strings.Contains(string(event.Payload), "private-identity") || strings.Contains(string(event.Payload), "257.1") {
				t.Fatal("values exposed")
			}
			found = true
		}
		if event.Kind == "failure" {
			var p ops.FailurePayload
			_ = json.Unmarshal(event.Payload, &p)
			failed = failed || p.Code == "stale_plan"
		}
	}
	if !found || !failed {
		t.Fatal("diagnostic or stale_plan failure missing", status.Events)
	}
}
