package inventory

import (
	"context"
	"testing"

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
