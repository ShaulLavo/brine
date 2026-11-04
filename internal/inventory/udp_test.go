package inventory

import (
	"bytes"
	"context"
	"encoding/json"
	"slices"
	"testing"

	"github.com/ShaulLavo/brine/internal/plan"
	"github.com/ShaulLavo/brine/internal/target"
)

func TestUDPExtendedSocketsPreserveHTTPUnionAndNeverInferOwnership(t *testing.T) {
	collector := Collector{FS: baseFixture(), Runner: fakeRunner{
		"ss -H -ltnpe": "LISTEN 0 128 127.0.0.1:4433 *:* users:((\"pasta\",pid=20,fd=7)) ino:91",
		"ss -H -lunpe": "UNCONN 0 0 [::]:4433 *:* users:((\"pasta\",pid=20,fd=8)) ino:92\nUNCONN 0 0 192.0.2.10:4434 *:* users:((\"pasta\",pid=20,fd=9))\nESTAB 0 0 192.0.2.10:50000 192.0.2.11:53",
	}}
	s := target.Snapshot{UDPSockets: unknown[[]target.UDPSocket]()}
	collector.listeners(context.Background(), &s, "/home/brine", nil)
	if s.UDPSockets.Status != target.KnownStatus || len(*s.UDPSockets.Value) != 2 || len(*s.UsedPorts.Value) != 2 {
		t.Fatal("UDP or conservative occupancy lost", s)
	}
	for _, socket := range *s.UDPSockets.Value {
		if socket.App != "" {
			t.Fatal("unproven socket owned", socket)
		}
	}
	if socketInode("UNCONN 0 0 192.0.2.10:4433 *:* ino:92") != "92" || socketInode("UNCONN 0 0 192.0.2.10:4433 *:*") != "" {
		t.Fatal("extended inode evidence lost")
	}
}
func TestLocalAddressObservationBoundsAndUnknowns(t *testing.T) {
	for _, test := range []struct {
		raw    string
		status target.Status
	}{
		{`[{"addr_info":[{"local":"192.0.2.10"},{"local":"2001:db8::1"},{"local":"192.0.2.10"}]}]`, target.KnownStatus},
		{`[]`, target.KnownStatus}, {`[{"addr_info":null}]`, target.Unknown}, {`null`, target.Unknown}, {`[{"addr_info":[{"local":"broken"}]}]`, target.Unknown}, {`[`, target.Unknown},
	} {
		c := Collector{Runner: fakeRunner{"ip -j addr show": test.raw}}
		got := c.localAddresses(t.Context())
		if got.Status != test.status {
			t.Fatal(test.raw, got)
		}
	}
}

func TestUDPUnrelatedKernelAddressesPreserveHTTPFacts(t *testing.T) {
	for _, address := range []string{"[fe80::1]%eth0", "224.0.0.251", "[::ffff:192.0.2.10]", "unparsed-address"} {
		t.Run(address, func(t *testing.T) { checkUDPUnrelatedKernelAddress(t, address) })
	}
}

func checkUDPUnrelatedKernelAddress(t *testing.T, address string) {
	t.Helper()
	c := Collector{FS: baseFixture(), Runner: fakeRunner{
		"ss -H -ltnpe": "LISTEN 0 128 127.0.0.1:22000 *:*",
		"ss -H -lunpe": "UNCONN 0 0 " + address + ":5353 *:*",
	}}
	in := routePlanInput(t, "ready-arm64")
	baseline, err := plan.Build(in)
	if err != nil || baseline.Kind != plan.Create {
		t.Fatal("invalid HTTP-only fixture", baseline, err)
	}
	s := in.Snapshot
	c.listeners(t.Context(), &s, "/home/brine", nil)
	if s.UsedPorts.Status != target.KnownStatus || !slices.Contains(*s.UsedPorts.Value, 5353) || s.PortOwners.Status != target.KnownStatus {
		t.Fatal("unrelated UDP address erased HTTP admission facts", s.UsedPorts, s.PortOwners)
	}
	if address == "unparsed-address" && s.UDPSockets.Status != target.Unknown {
		t.Fatal("unmodeled UDP evidence must not be a known empty set", s.UDPSockets)
	}
	if address != "unparsed-address" && (s.UDPSockets.Status != target.KnownStatus || len(*s.UDPSockets.Value) != 1) {
		t.Fatal("valid UDP observation lost", s.UDPSockets)
	}
	in.Snapshot = s
	actual, err := plan.Build(in)
	if err != nil || actual.Kind != plan.Create {
		t.Fatal("unrelated UDP address blocked HTTP-only planning", actual, err)
	}
	before, beforeErr := json.Marshal(baseline)
	after, afterErr := json.Marshal(actual)
	if beforeErr != nil || afterErr != nil || !bytes.Equal(before, after) {
		t.Fatal("unrelated UDP address changed HTTP-only plan bytes", baseline, actual, beforeErr, afterErr)
	}
}
