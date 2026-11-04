package inventory

import (
	"testing"

	"github.com/ShaulLavo/brine/internal/target"
)

// Row shapes follow iproute2 6.x `ss -H -lunpe` on Debian 13, where a socket
// bound to a device prints its local endpoint as host%device:port. Addresses
// and inodes are documentation values, not the captured host's inventory.
const scopedUDPCapture = `UNCONN 0      0                            192.0.2.10:68    0.0.0.0:* ino:3567 sk:1001 cgroup:/system.slice/networking.service <->
UNCONN 0      0                               0.0.0.0:41641 0.0.0.0:* ino:8905 sk:1002 cgroup:/system.slice/tailscaled.service <->
UNCONN 0      0                                     *:443         *:* uid:988 ino:4039207 sk:100d cgroup:/system.slice/caddy.service v6only:0 <->
UNCONN 0      0                        [2001:db8::1]:546      [::]:* ino:3591 sk:1003 cgroup:/system.slice/networking.service v6only:1 <->
UNCONN 0      0                        [fe80::1]%eth0:546      [::]:* ino:2976 sk:1004 cgroup:/system.slice/networking.service v6only:1 <->
UNCONN 0      0                                  [::]:41641    [::]:* ino:8902 sk:1005 cgroup:/system.slice/tailscaled.service v6only:1 <->`

func TestUDPDeviceScopedCaptureStaysKnown(t *testing.T) {
	s := target.Snapshot{UDPSockets: unknown[[]target.UDPSocket]()}
	(Collector{FS: baseFixture(), Runner: fakeRunner{"ss -H -ltnpe": "", "ss -H -lunpe": scopedUDPCapture}}).listeners(t.Context(), &s, "/home/brine", nil)
	want := []target.UDPSocket{{Address: "*", Port: 443}, {Address: "0.0.0.0", Port: 41641}, {Address: "192.0.2.10", Port: 68}, {Address: "2001:db8::1", Port: 546}, {Address: "::", Port: 41641}, {Address: "fe80::1", Interface: "eth0", Port: 546}}
	if s.UDPSockets.Status != target.KnownStatus || !sameUDPSockets(*s.UDPSockets.Value, want) {
		t.Fatalf("sockets=%+v", s.UDPSockets)
	}
}

func TestUDPDeviceScopedEndpointGrammar(t *testing.T) {
	for _, tc := range []struct {
		endpoint string
		want     *target.UDPSocket
	}{
		{"[fe80::1]%eth0:546", &target.UDPSocket{Address: "fe80::1", Interface: "eth0", Port: 546}},
		{"[FE80::0001]%enp0s31f6:546", &target.UDPSocket{Address: "fe80::1", Interface: "enp0s31f6", Port: 546}},
		{"[::]%br-1a2b.100:5353", &target.UDPSocket{Address: "::", Interface: "br-1a2b.100", Port: 5353}},
		{"0.0.0.0%eth0:68", &target.UDPSocket{Address: "0.0.0.0", Interface: "eth0", Port: 68}},
		{"192.0.2.10%wg_0:51820", &target.UDPSocket{Address: "192.0.2.10", Interface: "wg_0", Port: 51820}},
		{"*%tailscale0:41641", &target.UDPSocket{Address: "*", Interface: "tailscale0", Port: 41641}},
		{"[::ffff:192.0.2.10]:5353", &target.UDPSocket{Address: "192.0.2.10", Port: 5353}},
		{"[fe80::1%eth0]:546", nil},
		{"fe80::1%eth0:546", nil},
		{"[fe80::1]%:546", nil},
		{"[fe80::1]%eth0%eth1:546", nil},
		{"[fe80::1]%eth0]:546", nil},
		{"[fe80::1]%%eth0:546", nil},
		{"[fe80::1]%abcdefghijklmnop:546", nil},
		{"[fe80::1]%..:546", nil},
		{"[fe80::1]%-eth0:546", nil},
		{"[fe80::1]%eth/0:546", nil},
		{"[[fe80::1]]%eth0:546", nil},
		{"[fe80::1:546", nil},
		{"fe80::1]%eth0:546", nil},
		{"[192.0.2.10]%eth0:68", nil},
		{"[*]%eth0:68", nil},
		{"%eth0:68", nil},
		{"*%:68", nil},
		{"192.0.2.10%:68", nil},
	} {
		t.Run(tc.endpoint, func(t *testing.T) {
			s := target.Snapshot{UDPSockets: unknown[[]target.UDPSocket]()}
			r := fakeRunner{"ss -H -ltnpe": "", "ss -H -lunpe": "UNCONN 0 0 192.0.2.20:5000 0.0.0.0:*\nUNCONN 0 0 " + tc.endpoint + " [::]:* ino:2976"}
			(Collector{FS: baseFixture(), Runner: r}).listeners(t.Context(), &s, "/home/brine", nil)
			if tc.want == nil {
				if s.UDPSockets.Status != target.Unknown {
					t.Fatalf("malformed endpoint became known: %+v", s.UDPSockets)
				}
				return
			}
			want := []target.UDPSocket{{Address: "192.0.2.20", Port: 5000}, *tc.want}
			if s.UDPSockets.Status != target.KnownStatus || !sameUDPSockets(*s.UDPSockets.Value, want) {
				t.Fatalf("sockets=%+v want=%+v", s.UDPSockets, want)
			}
		})
	}
}

func TestUDPDeviceScopedSocketsKeepDistinctIdentity(t *testing.T) {
	s := target.Snapshot{UDPSockets: unknown[[]target.UDPSocket]()}
	r := fakeRunner{"ss -H -ltnpe": "", "ss -H -lunpe": "UNCONN 0 0 0.0.0.0%eth0:68 0.0.0.0:*\nUNCONN 0 0 0.0.0.0%eth1:68 0.0.0.0:*\nUNCONN 0 0 0.0.0.0:68 0.0.0.0:*"}
	(Collector{FS: baseFixture(), Runner: r}).listeners(t.Context(), &s, "/home/brine", nil)
	if s.UDPSockets.Status != target.KnownStatus || len(*s.UDPSockets.Value) != 3 {
		t.Fatalf("device-scoped sockets collapsed: %+v", s.UDPSockets)
	}
	r["ss -H -lunpe"] = "UNCONN 0 0 0.0.0.0%eth0:68 0.0.0.0:*\nUNCONN 0 0 0.0.0.0%eth0:68 0.0.0.0:*"
	(Collector{FS: baseFixture(), Runner: r}).listeners(t.Context(), &s, "/home/brine", nil)
	if s.UDPSockets.Status != target.Unknown {
		t.Fatalf("identical scoped sockets became known: %+v", s.UDPSockets)
	}
}

func sameUDPSockets(got, want []target.UDPSocket) bool {
	seen := map[target.UDPSocket]bool{}
	for _, socket := range got {
		seen[socket] = true
	}
	for _, socket := range want {
		if !seen[socket] {
			return false
		}
	}
	return len(got) == len(want) && len(seen) == len(want)
}
