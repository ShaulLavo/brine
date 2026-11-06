package cli

import (
	"bytes"
	"context"
	"slices"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"encoding/json"
	"github.com/ShaulLavo/brine/internal/dispatch"
	"github.com/ShaulLavo/brine/internal/jobs"
	"github.com/ShaulLavo/brine/internal/ops"
	"github.com/ShaulLavo/brine/internal/result"
	"github.com/ShaulLavo/brine/internal/transport"
)

func TestTUIConnectedOperationUsesStrictSharedRead(t *testing.T) {
	for _, bad := range []string{"none", "empty cursor drift", "wrong ID", "duplicate sequence", "at cursor", "value payload"} {
		t.Run(bad, func(t *testing.T) { testStrictOperationRead(t, bad) })
	}
}

func testStrictOperationRead(t *testing.T, bad string) {
	t.Helper()
	var out, diagnostics bytes.Buffer
	deps := testDependencies(t, &out, &diagnostics)
	deps.LoadOperationTarget = func(_ string, name string) (transport.Target, error) { return transport.Target{Name: name}, nil }
	var requests []dispatch.Request
	deps.OperationClient = callFunc(func(_ context.Context, _ transport.Target, req dispatch.Request) (result.Envelope, error) {
		req.Args = slices.Clone(req.Args)
		requests = append(requests, req)
		status := operationFixture(ops.Starting, 7, 1)
		switch bad {
		case "empty cursor drift":
			status.Events = []ops.Event{}
			status.NextCursor = 9
		case "wrong ID":
			status.Operation.ID = "other"
		case "duplicate sequence":
			status.Events = append(status.Events, status.Events[0])
		case "at cursor":
			status.Events[0].Sequence = 7
			status.NextCursor = 7
		case "value payload":
			status.Events[0].Kind = "step"
			status.Events[0].State = ""
			status.Events[0].Payload = json.RawMessage(`{"step":"start_unit","outcome":"completed","password":"hunter2"}`)
		}
		raw, err := json.Marshal(result.Success("brine host operation", status))
		if err != nil {
			t.Fatal(err)
		}
		return dispatch.DecodeResponse(raw, req.Op)
	})
	host := connectedHost{deps: deps, flags: operationFlags{target: "fixture"}}
	status, err := host.Operation(t.Context(), "op-01fixture", 7)
	if bad == "none" && (err != nil || status.NextCursor != 8) {
		t.Fatalf("valid read: %+v %v", status, err)
	}
	if bad != "none" && result.Classify(err).Code() != result.TransportInvalidResponse {
		t.Fatalf("bad page accepted: %+v %v", status, err)
	}
	if len(requests) != 1 || requests[0].Op != "operation" || string(requests[0].Args) != `{"operation_id":"op-01fixture","after_cursor":7}` {
		t.Fatalf("unexpected requests: %+v", requests)
	}
}

func TestTUIOperationNavigationCancelsAndDropsStaleTargetPagesAndTicks(t *testing.T) {
	host := fixtureHost()
	started, canceled := make(chan string, 1), make(chan string, 1)
	host.operation = func(ctx context.Context, id string, _ uint64) (jobs.Status, error) {
		started <- id
		<-ctx.Done()
		canceled <- id
		return operationFixture(ops.Succeeded, 0, 1), nil
	}
	m, cmd := observing(host)
	oldToken := m.token
	reply := make(chan tea.Msg, 1)
	go func() { reply <- cmd() }()
	receive(t, started, "op-01fixture")
	m, _ = updateTUI(m, keyMsg("esc"))
	receive(t, canceled, "op-01fixture")
	m.flags = operationFlags{target: "elsewhere"}
	m, cmd = updateTUI(m, <-reply)
	if cmd != nil || m.observe.Refreshed || m.observe.Cursor != 0 || m.flags.target != "elsewhere" {
		t.Fatal("stale target response changed model")
	}
	m, cmd = updateTUI(m, pollMsg{token: oldToken})
	if cmd != nil || !slices.Equal(host.Calls(), []string{"apps", "operation op-01fixture 0"}) {
		t.Fatalf("stale poll issued a read: %v", host.Calls())
	}
	if !strings.Contains(m.exitNotice(), "--target fixture") {
		t.Fatalf("original target lost: %s", m.exitNotice())
	}
}
