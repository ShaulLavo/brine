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
