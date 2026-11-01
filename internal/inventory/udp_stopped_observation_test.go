//go:build linux

package inventory

import (
	"context"
	"strings"
	"testing"

	"github.com/ShaulLavo/brine/internal/target"
	"github.com/ShaulLavo/brine/internal/udpready"
)

type stoppedRuntimeCase struct {
	name, inspect, listing string
	wantStopped            bool
}

func TestContradictoryStoppedRuntimeRefusesAbsence(t *testing.T) {
	for _, tc := range []stoppedRuntimeCase{
		{"running", udpRuntimeRecord, "", false},
		{"retained bindings", strings.Replace(udpRuntimeRecord, `"running":true`, `"running":false`, 1), "", false},
		{"malformed", `{`, "", false},
		{"missing running", `{"name":"systemd-api","unit":"api.service","ports":{}}`, "", false},
		{"missing ports", `{"name":"systemd-api","running":false,"unit":"api.service"}`, "", false},
		{"overflow", strings.Repeat("x", runtimeOutputLimit+1), "", false},
		{"unavailable inspect, absent listing", "", "", true},
		{"unavailable inspect, retained container", "", "systemd-api api.service", false},
		{"observed stopped", `{"name":"systemd-api","running":false,"unit":"api.service","ports":{}}`, "systemd-api api.service", true},
	} {
		t.Run(tc.name, func(t *testing.T) { checkStoppedRuntimeObservation(t, tc) })
	}
}

func checkStoppedRuntimeObservation(t *testing.T, tc stoppedRuntimeCase) {
	t.Helper()
	f, runner, control, current, state := udpCollectorFixture()
	control.Apps[0].UDP = &target.ControlUDPRelease{Incarnation: state.Incarnations[0].ID, PlanID: state.Incarnations[0].ID, Units: current.Units, Publication: target.UDPPublication{BindAddress: "192.0.2.10", HostPort: 4433, ContainerPort: 7777, NetworkMode: "pasta"}}
	control.UDPReservations = []target.ControlUDPReservation{{ID: "udp-op", App: "api", Incarnation: state.Incarnations[0].ID, State: "active", Family: "ipv4", BindAddress: "192.0.2.10", HostPort: 4433}}
	runner["systemctl --user show api.service --property=ActiveState --value"] = "inactive"
	runner["podman --remote=false ps --all --format "+containerInventoryFormat] = tc.listing
	runner["ss -H -lunpe"] = ""
	inspect := "podman --remote=false inspect --type container --format " + testRuntimePortFormat + " systemd-api"
	delete(runner, inspect)
	if tc.inspect != "" {
		runner[inspect] = tc.inspect
	}
	collector := Collector{FS: f, Runner: runner, IdentityKey: []byte("fixture"), StateInventory: func(context.Context) (target.ControlInventory, error) { return control, nil }}
	snapshot, err := collector.Collect(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	result := udpready.Check(snapshot, "api", *current.UDP, current.Units)
	if (result.Local == udpready.Stopped) != tc.wantStopped {
		t.Fatalf("inconsistent runtime observation: local=%+v wantStopped=%v", result, tc.wantStopped)
	}
}
