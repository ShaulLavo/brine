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
	r.inventory.snapshot.UsedPorts = target.Known([]target.Port{20010})
	planned := r.call(t, "plan", dispatch.PlanArgs{Spec: r.spec}).Data.(dispatch.Planned)
	accepted := r.call(t, "apply", dispatch.ApplyArgs{PlanID: planned.PlanID, IdempotencyKey: "drift-diagnostic"}).Data.(jobs.Accepted)
	r.inventory.snapshot.Identity.ID = "private-identity"
	r.inventory.snapshot.UsedPorts = target.Known([]target.Port{20011})
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

func TestDetachedListenerFreshnessBeforeEffects(t *testing.T) {
	for _, tc := range []struct {
		name       string
		port       target.Port
		tcp, stale bool
	}{
		{"ephemeral UDP churn", 55650, false, false},
		{"new TCP listener", 20000, true, true},
		{"new UDP listener", 20000, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newDeployRig(t)
			r.inventory.snapshot.UsedPorts = target.Known([]target.Port{45006})
			planned := r.call(t, "plan", dispatch.PlanArgs{Spec: r.spec}).Data.(dispatch.Planned)
			accepted := r.call(t, "apply", dispatch.ApplyArgs{PlanID: planned.PlanID, IdempotencyKey: "listener-freshness"}).Data.(jobs.Accepted)
			r.inventory.snapshot.UsedPorts = target.Known([]target.Port{tc.port})
			if tc.tcp {
				r.inventory.snapshot.PortOwners = target.Known([]target.PortOwner{{Port: tc.port, Process: "foreign"}})
			}
			err := r.runner.Run(t.Context(), accepted.OperationID)
			status := r.call(t, "operation", dispatch.OperationArgs{OperationID: accepted.OperationID}).Data.(jobs.Status)
			if tc.stale {
				if err == nil || status.Operation.State != ops.Failed || r.pulls != 0 || r.units.installs != 0 {
					t.Fatal("listener drift was not refused before effects", err, status.Operation.State)
				}
				failure := false
				for _, event := range status.Events {
					if event.Kind == "failure" {
						var payload ops.FailurePayload
						if json.Unmarshal(event.Payload, &payload) != nil {
							t.Fatal("invalid failure payload")
						}
						failure = payload.Code == "stale_plan"
					}
				}
				if !failure {
					t.Fatal("missing stale_plan failure")
				}
			} else if err != nil || status.Operation.State != ops.Succeeded || r.pulls != 1 || r.units.installs != 1 {
				t.Fatal("ephemeral churn refused healthy deployment", err, status.Operation.State)
			}
		})
	}
}
