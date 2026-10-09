package dispatch

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/ShaulLavo/brine/internal/result"
)

func TestStrictResponseDecoding(t *testing.T) {
	response := result.Success("brine host ping", PingData{"test", []int{1}})
	raw, err := json.Marshal(response)
	if err != nil {
		t.Fatal(err)
	}
	good := string(raw)
	if _, err := DecodeResponse(raw, "ping"); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{
		strings.Replace(good, "schema_version", "Schema_Version", 1),
		strings.Replace(good, `"schema_version":1`, `"schema_version":2`, 1),
		strings.Replace(good, `"ok":true`, `"ok":true,"ok":false`, 1),
		strings.Replace(good, `"error":null`, `"error":{}`, 1),
		strings.Replace(good, "server_version", "Server_Version", 1),
		strings.Replace(good, `"protocol_versions":[1]`, `"protocol_versions":null`, 1),
		strings.Replace(good, `"protocol_versions":[1]`, `"protocol_versions":[null]`, 1),
		strings.Replace(good, `"protocol_versions":[1]`, `"protocol_versions":[2]`, 1),
		strings.Replace(good, "brine host ping", "brine host inventory", 1),
		good + good,
		good + strings.Repeat(" ", ResponseLimit),
	} {
		if _, err := DecodeResponse([]byte(bad), "ping"); result.ExitCode(err) != 1 {
			t.Fatalf("accepted %s", bad)
		}
	}
	failure, _ := json.Marshal(result.Failure("brine host serve", result.New(result.DispatchOperationRefused, nil)))
	if _, err := DecodeResponse(failure, "ping"); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{
		strings.Replace(string(failure), "operation is not allowed", "operation is unsafe", 1),
		strings.Replace(string(failure), "dispatch_operation_refused", "unknown_code", 1),
		strings.Replace(string(failure), `"retryable":false`, `"retryable":null`, 1),
		strings.Replace(string(failure), `"code":`, `"Code":`, 1),
		strings.Replace(string(failure), `"data":null`, `"data":{}`, 1),
	} {
		if _, err := DecodeResponse([]byte(bad), "ping"); err == nil {
			t.Fatalf("accepted %s", bad)
		}
	}
}

func TestUnavailableAndInvalidInventory(t *testing.T) {
	input := `{"schema_version":1,"op":"inventory","request_id":"test","args":{}}`
	for _, tt := range []struct {
		provider Inventory
		code     result.Code
	}{
		{nil, result.DependencyMissing},
		{&inventoryFake{}, result.InternalError},
	} {
		response, err := NewServer("test", tt.provider).Handle(t.Context(), strings.NewReader(input))
		if err == nil || response.Error.Code != tt.code {
			t.Fatalf("%+v %v", response, err)
		}
	}
}
