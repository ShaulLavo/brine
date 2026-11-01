//go:build linux

package inventory

import (
	"context"
	"strings"
	"testing"

	"github.com/ShaulLavo/brine/internal/target"
)

func TestOperationUDPProofUsesKernelIdentityWithoutCommittedHead(t *testing.T) {
	for _, name := range []string{"ready", "no inode", "wrong FD", "wrong marker", "extra socket", "wrong units"} {
		t.Run(name, func(t *testing.T) {
			f, r, control, current, state := udpCollectorFixture()
			id := state.Incarnations[0].ID
			control.Apps[0].UDP = nil
			control.UDPReservations = []target.ControlUDPReservation{{ID: "udp-operation", App: "api", Incarnation: id, State: "staged", Owner: "operation", Family: "ipv4", BindAddress: "192.0.2.10", HostPort: 4433}}
			switch name {
			case "no inode":
				r["ss -H -lunpe"] = strings.ReplaceAll(udpOwnedLine, " ino:77777", "")
			case "wrong FD":
				f.links["/proc/4242/fd/6"] = "socket:[88888]"
			case "wrong marker":
				id = "sha256:" + strings.Repeat("b", 64)
			case "extra socket":
				r["ss -H -lunpe"] += "\nUNCONN 0 0 [::]:4433 *:*"
			case "wrong units":
				current.Units[0].Hash = "sha256:" + strings.Repeat("b", 64)
			}
			c := Collector{FS: f, Runner: r, IdentityKey: []byte("fixture"), StateInventory: func(context.Context) (target.ControlInventory, error) { return control, nil }}
			err := c.UDPReady(t.Context(), "api", id, *current.UDP, current.Units)
			if (err == nil) != (name == "ready") {
				t.Fatalf("%s: %v", name, err)
			}
			snapshot, err := c.Collect(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			if snapshot.UDPSockets.Value != nil && (*snapshot.UDPSockets.Value)[0].App != "" {
				t.Fatal("operation proof changed status ownership")
			}
		})
	}
}
func TestUDPAbsentRequiresUnitRuntimeAndSocketAbsence(t *testing.T) {
	for _, name := range []string{"ready", "unit remnant", "container remnant", "socket remnant", "unknown sockets", "unknown inspect"} {
		t.Run(name, func(t *testing.T) {
			f, r, _, current, _ := udpCollectorFixture()
			inspect := "podman --remote=false inspect --type container --format " + runtimePortFormat + " systemd-api"
			f.files["/home/brine/.config/containers/systemd/api.container"] = strings.ReplaceAll(udpOwnedUnit, "192.0.2.10:4433:7777", "192.0.2.10:4434:7777")
			r[inspect] = strings.ReplaceAll(udpRuntimeRecord, `"HostPort":"4433"`, `"HostPort":"4434"`)
			r["ss -H -lunpe"] = ""
			switch name {
			case "unit remnant":
				f.files["/home/brine/.config/containers/systemd/api.container"] = udpOwnedUnit
			case "container remnant":
				r[inspect] = udpRuntimeRecord
			case "socket remnant":
				r["ss -H -lunpe"] = udpOwnedLine
			case "unknown sockets":
				delete(r, "ss -H -lunpe")
			case "unknown inspect":
				r[inspect] = "{"
			}
			c := Collector{FS: f, Runner: r, IdentityKey: []byte("fixture")}
			err := c.UDPAbsent(t.Context(), "api", current.UDP.Endpoint)
			if (err == nil) != (name == "ready") {
				t.Fatalf("%s: %v", name, err)
			}
		})
	}
}
