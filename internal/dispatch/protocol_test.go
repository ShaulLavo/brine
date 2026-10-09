package dispatch

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/ShaulLavo/brine/internal/result"
	"github.com/ShaulLavo/brine/internal/target"
)

type inventoryFake struct{ calls int }

func (f *inventoryFake) Collect(context.Context) (target.Snapshot, error) {
	f.calls++
	return target.Snapshot{}, nil
}

func TestRequestRefusals(t *testing.T) {
	good := `{"schema_version":1,"op":"ping","request_id":"test-1","args":{}}`
	for _, tt := range []struct {
		name, input string
		code        result.Code
		exit        int
	}{
		{"unknown op", strings.Replace(good, "ping", "shell", 1), result.DispatchOperationRefused, 4},
		{"schema", strings.Replace(good, `:1`, `:2`, 1), result.DispatchUnsupportedSchema, 3},
		{"case", strings.Replace(good, "op", "Op", 1), result.DispatchInvalidRequest, 2},
		{"unknown field", strings.Replace(good, `"args":{}`, `"args":{},"command":"shell"`, 1), result.DispatchInvalidRequest, 2},
		{"duplicate", strings.Replace(good, `"op":"ping"`, `"op":"inventory","op":"ping"`, 1), result.DispatchInvalidRequest, 2},
		{"null args", strings.Replace(good, `"args":{}`, `"args":null`, 1), result.DispatchInvalidRequest, 2},
		{"typed args", strings.Replace(good, `"args":{}`, `"args":{"shell":"ignored"}`, 1), result.DispatchInvalidRequest, 2},
		{"second request", good + good, result.DispatchInvalidRequest, 2},
		{"trailing", good + "junk", result.DispatchInvalidRequest, 2},
		{"oversize", good + strings.Repeat(" ", RequestLimit), result.DispatchInvalidRequest, 2},
		{"missing id", strings.Replace(good, `"request_id":"test-1",`, "", 1), result.DispatchInvalidRequest, 2},
	} {
		t.Run(tt.name, func(t *testing.T) {
			fake := &inventoryFake{}
			response, err := NewServer("test", fake).Handle(context.Background(), strings.NewReader(tt.input))
			if result.ExitCode(err) != tt.exit || response.Error == nil || response.Error.Code != tt.code || fake.calls != 0 {
				t.Fatalf("response=%+v err=%v calls=%d", response, err, fake.calls)
			}
		})
	}
}

func TestPingAndInventory(t *testing.T) {
	server := NewServer("test-version", nil)
	response, err := server.Handle(context.Background(), strings.NewReader(`{"schema_version":1,"op":"ping","request_id":"test","args":{}}`))
	if err != nil || !response.OK {
		t.Fatalf("%+v %v", response, err)
	}
	data := response.Data.(PingData)
	if data.ServerVersion != "test-version" || len(data.ProtocolVersions) != 1 || data.ProtocolVersions[0] != 1 {
		t.Fatalf("%+v", data)
	}
	if class, ok := ClassOf("ping"); !ok || class != ReadOnly {
		t.Fatal("ping class")
	}
	if _, ok := ClassOf("shell"); ok {
		t.Fatal("unknown registered")
	}
	fixture, err := target.Decode(mustFixture(t))
	if err != nil {
		t.Fatal(err)
	}
	fake := snapshotInventory{fixture}
	response, err = NewServer("test", fake).Handle(context.Background(), strings.NewReader(`{"schema_version":1,"op":"inventory","request_id":"test","args":{}}`))
	if err != nil || !response.OK {
		t.Fatalf("%+v %v", response, err)
	}
	raw, _ := json.Marshal(response)
	if _, err := DecodeResponse(raw, "inventory"); err != nil {
		t.Fatal(err)
	}
}

type snapshotInventory struct{ snapshot target.Snapshot }

func (f snapshotInventory) Collect(context.Context) (target.Snapshot, error) { return f.snapshot, nil }

func FuzzRequest(f *testing.F) {
	f.Add([]byte(`{"schema_version":1,"op":"ping","request_id":"test","args":{}}`))
	f.Add([]byte(`{"schema_version":1,"op":"shell","request_id":"test","args":{}}`))
	f.Fuzz(func(t *testing.T, data []byte) {
		fake := &inventoryFake{}
		response, _ := NewServer("test", fake).Handle(context.Background(), bytes.NewReader(data))
		if fake.calls != 0 {
			request, err := DecodeRequest(data)
			if err != nil || request.Op != "inventory" {
				t.Fatal("unvalidated dispatch")
			}
		}
		if response.OK && response.Command != "brine host ping" {
			t.Fatal("unknown operation succeeded")
		}
	})
}
