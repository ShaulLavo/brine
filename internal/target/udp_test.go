package target

import (
	"bytes"
	"testing"
)

func TestUDPObservationsRequiredOnWire(t *testing.T) {
	raw := fixture(t, "one-app")
	for _, field := range []string{`"udp_sockets":{"status":"known","value":[]},`, `"local_addresses":{"status":"known","value":[]},`, `,"udp_publication":{"status":"absent"}`} {
		changed := bytes.Replace(raw, []byte(field), nil, 1)
		if bytes.Equal(changed, raw) {
			t.Fatal("missing fixture field", field)
		}
		if _, err := Decode(changed); err == nil {
			t.Fatal("accepted missing UDP observation", field)
		}
	}
}

func TestUDPSocketInterfaceValidation(t *testing.T) {
	for _, tc := range []struct {
		socket UDPSocket
		valid  bool
	}{
		{UDPSocket{Address: "fe80::1", Interface: "eth0", Port: 546}, true},
		{UDPSocket{Address: "0.0.0.0", Interface: "br-1a2b.100", Port: 68}, true},
		{UDPSocket{Address: "fe80::1", Interface: "abcdefghijklmnop", Port: 546}, false},
		{UDPSocket{Address: "fe80::1", Interface: "eth0%1", Port: 546}, false},
		{UDPSocket{Address: "fe80::1", Interface: "-eth0", Port: 546}, false},
		{UDPSocket{Address: "fe80::1%eth0", Port: 546}, false},
		{UDPSocket{Address: "192.0.2.10", Interface: "eth0", Port: 4433, App: "api"}, false},
	} {
		if err := validateUDPSockets([]UDPSocket{tc.socket}); (err == nil) != tc.valid {
			t.Fatal(tc.socket, err)
		}
	}
	if validateUDPSockets([]UDPSocket{{Address: "0.0.0.0", Interface: "eth0", Port: 68}, {Address: "0.0.0.0", Interface: "eth1", Port: 68}}) != nil {
		t.Fatal("distinct device-scoped sockets rejected")
	}
}
