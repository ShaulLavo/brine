package enroll

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	operatorpolicy "github.com/ShaulLavo/brine/internal/policy"
	"github.com/ShaulLavo/brine/internal/result"
	"github.com/ShaulLavo/brine/internal/spec"
)

func TestUDPGrantPlanApplyAndRevocationRechecksReservations(t *testing.T) {
	service, memory, _ := permissionFixture(t)
	g := operatorpolicy.UDPGrant{App: "demo", Exposure: spec.UDPPublic, BindAddress: "192.0.2.10", ClientAddress: "relay.example.test", HostPorts: operatorpolicy.PortRange{Min: 4433, Max: 4433}}
	req := PermissionsRequest{Action: "plan", Changes: []operatorpolicy.PermissionAction{{Kind: operatorpolicy.AllowUDPGrant, UDPGrant: &g}}}
	response, err := service.run(t.Context(), req)
	if err != nil {
		t.Fatal(err)
	}
	wire, err := json.Marshal(result.Success("brine host permissions", response))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := decodePermissionsResponse(wire, req); err != nil {
		t.Fatal("UDP permission wire failed", err)
	}
	if _, err = service.run(t.Context(), permissionApply(*response.Plan, "a")); err != nil {
		t.Fatal(err)
	}
	req.Changes = []operatorpolicy.PermissionAction{{Kind: operatorpolicy.RevokeUDPGrant, UDPApp: "demo"}}
	held := false
	service.udpHeld = func(context.Context, string) (bool, error) { return held, nil }
	revocation, err := service.run(t.Context(), req)
	if err != nil {
		t.Fatal(err)
	}
	held = true
	writes := memory.writes
	operations := len(memory.ops)
	if _, err := service.run(t.Context(), permissionApply(*revocation.Plan, "b")); err == nil || memory.writes != writes || len(memory.ops) != operations {
		t.Fatal("reservation race allowed revocation", err)
	}
	if _, err := service.run(t.Context(), req); err == nil {
		t.Fatal("live reservation allowed revoke plan")
	}
	service.udpHeld = func(context.Context, string) (bool, error) { return false, errors.New("unreadable") }
	if _, err := service.run(t.Context(), req); err == nil {
		t.Fatal("unknown reservations allowed revoke")
	}
	held = false
	service.udpHeld = func(context.Context, string) (bool, error) { return held, nil }
	if _, err := service.run(t.Context(), permissionApply(*revocation.Plan, "b")); err != nil {
		t.Fatal("settled revocation could not resume", err)
	}
}
func TestUDPGrantWireRefusesMixedPayloads(t *testing.T) {
	for _, raw := range []string{
		`{"kind":"revoke_udp_grant","udp_app":"demo","domain":"example.test"}`,
		`{"kind":"revoke_udp_grant","udp_app":null}`,
		`{"kind":"revoke_udp_grant","udp_app":"demo","udp_app":"other"}`,
		`{"kind":"revoke_udp_grant","UDPApp":"demo"}`,
	} {
		if _, err := decodePermissionChange([]byte(raw)); err == nil {
			t.Fatal("malformed UDP grant accepted")
		}
	}
}
