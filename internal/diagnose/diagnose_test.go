package diagnose

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/ShaulLavo/brine/internal/ops"
	"github.com/ShaulLavo/brine/internal/target"
)

func TestRules(t *testing.T) {
	cases := []struct {
		code   string
		report Report
	}{
		{"disk_below_minimum", Report{Host: Host{FreeDiskBytes: Known(uint64(1)), MinimumFreeDiskBytes: Known(uint64(2))}}},
		{"runner_linger_disabled", Report{Host: Host{Linger: Known(false)}}},
		{"unit_failed", Report{Apps: []App{{Name: "demo", Unit: Known(Unit{ActiveState: "failed"})}}}},
		{"unit_restarting", Report{Apps: []App{{Name: "demo", Unit: Known(Unit{ActiveState: "active", Restarts: 3})}}}},
		{"container_stopped", Report{Apps: []App{{Name: "demo", ContainerRunning: Known(false)}}}},
		{"route_missing", Report{Apps: []App{{Name: "demo", RoutePresent: Known(false)}}}},
		{"artifact_drift", Report{Apps: []App{{Name: "demo", Drift: Known([]string{"unit"})}}}},
		{"operation_failed", Report{Apps: []App{{Name: "demo", Operations: Known([]RecentOperation{{State: ops.Failed}})}}}},
		{"recovery_required", Report{Apps: []App{{Name: "demo", Operations: Known([]RecentOperation{{State: ops.RecoveryRequired}})}}}},
		{"plan_stale", Report{Apps: []App{{Name: "demo", Operations: Known([]RecentOperation{{State: ops.Failed, FailureCode: "stale_plan"}})}}}},
		{"health_failed", Report{Apps: []App{{Name: "demo", Health: Known(false)}}}},
	}
	for _, tt := range cases {
		t.Run(tt.code, func(t *testing.T) {
			found := false
			for _, f := range Findings(tt.report) {
				if f.Code == tt.code {
					found = true
					if f.Message == "" || len(f.NextOperations) == 0 || f.Severity == "" {
						t.Fatal(f)
					}
				}
			}
			if !found {
				t.Fatalf("missing %s", tt.code)
			}
		})
	}
	if len(Findings(Report{})) != 0 {
		t.Fatal("unknown facts must not trigger failure findings")
	}
}

type hangingInventory struct{ release <-chan struct{} }

func (h hangingInventory) Collect(context.Context) (target.Snapshot, error) {
	<-h.release
	return target.Snapshot{}, nil
}
func TestBoundedHungProbe(t *testing.T) {
	release := make(chan struct{})
	defer close(release)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	start := time.Now()
	report, err := (Reader{Inventory: hangingInventory{release}}).Read(ctx, Request{})
	if err == nil {
		t.Fatalf("expected caller cancellation, got %+v", report)
	}
	if time.Since(start) > time.Second {
		t.Fatal("hung probe escaped total deadline")
	}
}
func TestUnknownReasonsNeverExposeErrors(t *testing.T) {
	report, err := (Reader{}).Read(context.Background(), Request{})
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(report)
	if !strings.Contains(string(raw), "unknown") || !strings.Contains(string(raw), "reason") {
		t.Fatal(string(raw))
	}
}
