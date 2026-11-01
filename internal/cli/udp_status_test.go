package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ShaulLavo/brine/internal/apps"
	"github.com/ShaulLavo/brine/internal/dispatch"
	"github.com/ShaulLavo/brine/internal/plan"
	"github.com/ShaulLavo/brine/internal/result"
	"github.com/ShaulLavo/brine/internal/spec"
	"github.com/ShaulLavo/brine/internal/target"
	"github.com/ShaulLavo/brine/internal/transport"
	"github.com/ShaulLavo/brine/internal/udpready"
)

func udpAppResults(local udpready.State) *appOperations {
	service := appResults()
	allocation := plan.UDPAllocation{Endpoint: plan.UDPEndpoint{Family: "ipv4", BindAddress: "203.0.113.10", HostPort: 4433}, ContainerPort: 7777, ClientAddress: "game.example.com", Exposure: spec.UDPPublic, GrantHash: "sha256:" + strings.Repeat("c", 64)}
	incarnation := "sha256:" + strings.Repeat("d", 64)
	item := &service.report.Apps[0]
	item.Current.UDP = &allocation
	item.UDP = &apps.UDPStatus{Protocol: "udp", Exposure: allocation.Exposure, BindAddress: allocation.Endpoint.BindAddress, ClientAddress: allocation.ClientAddress, HostPort: 4433, ContainerPort: 7777, Incarnation: incarnation, Reservation: "active", Reservations: []plan.UDPReservation{{ID: "udp-op", App: item.App, Incarnation: incarnation, State: "active", Endpoint: allocation.Endpoint}}, Local: local, LocalReasons: []string{}, Remote: "unverified"}
	switch local {
	case udpready.NotReady:
		item.UDP.LocalReasons = []string{"socket_missing"}
	case udpready.Unknown:
		item.UDP.LocalReasons = []string{"observation_unknown"}
	case udpready.Stopped:
		item.Health.UnitActive = target.Known(false)
		item.Health.Direct = "not_checked"
	}
	return service
}

func TestUDPStatusPresentation(t *testing.T) {
	for _, local := range []udpready.State{udpready.Ready, udpready.NotReady, udpready.Stopped, udpready.Unknown} {
		t.Run(string(local), func(t *testing.T) { assertUDPStatusPresentation(t, local) })
	}
}
func assertUDPStatusPresentation(t *testing.T, local udpready.State) {
	t.Helper()
	for _, mode := range []string{"human", "json", "jsonl"} {
		checkUDPStatusGolden(t, local, mode)
	}
}
func checkUDPStatusGolden(t *testing.T, local udpready.State, mode string) {
	t.Helper()
	server := dispatch.NewServer("test", nil)
	server.Apps = udpAppResults(local)
	var out, stderr bytes.Buffer
	deps := testDependencies(t, &out, &stderr)
	deps.LoadOperationTarget = func(string, string) (transport.Target, error) { return transport.Target{Name: "fixture"}, nil }
	deps.OperationClient = callFunc(func(ctx context.Context, _ transport.Target, request dispatch.Request) (result.Envelope, error) {
		raw, err := dispatch.EncodeRequest(request)
		if err != nil {
			return result.Envelope{}, err
		}
		response, err := server.Handle(ctx, bytes.NewReader(raw))
		if err != nil {
			return result.Envelope{}, err
		}
		encoded, err := json.Marshal(response)
		if err != nil {
			return result.Envelope{}, err
		}
		return dispatch.DecodeResponse(encoded, request.Op)
	})
	args := []string{"status", "hello", "--target", "fixture"}
	if mode != "human" {
		args = append(args, "--"+mode)
	}
	if err := Execute(deps, args); err != nil {
		t.Fatal(err, out.String())
	}
	if mode == "human" && !strings.Contains(out.String(), "remote unverified") {
		t.Fatal("missing UDP presentation", out.String())
	}
	path := filepath.Join("testdata", "udp-status-"+string(local)+"."+mode)
	if os.Getenv("UPDATE_GOLDEN") == "1" {
		if err := os.WriteFile(path, out.Bytes(), 0600); err != nil {
			t.Fatal(err)
		}
	}
	want, err := fs.ReadFile(os.DirFS("testdata"), "udp-status-"+string(local)+"."+mode)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(want, out.Bytes()) {
		t.Fatalf("want %s\ngot %s", want, out.String())
	}
}
