package dispatch

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestConfigLifecycleAndSecretRequestBoundaries(t *testing.T) {
	for _, tc := range []struct {
		op, args string
		valid    bool
	}{
		{"config_set", `{"app":"hello","edits":[{"key":"environment.KEY","value":"private","action":""}]}`, true},
		{"config_set", `{"app":"hello","edits":[{"key":"environment.KEY","value":"private","action":"","Value":"private"}]}`, false},
		{"config_set", `{"app":"hello","edits":[]}`, false},
		{"config_set", `{"app":"hello","edits":null}`, false},
		{"lifecycle", `{"app":"hello","action":"stop_app"}`, true},
		{"lifecycle", `{"app":"hello","action":"remove_app"}`, false},
		{"lifecycle", `{"app":"hello","action":"start_app","unit":"other.service"}`, false},
		{"secret_set", `{"app":"hello","reference":"hello-token","value":"cHJpdmF0ZQ=="}`, true},
		{"secret_set", `{"app":"hello","reference":"hello-token","value":""}`, false},
		{"secret_set", `{"app":"hello","reference":"hello-token","value":[1,2]}`, false},
		{"secret_set", `{"app":"hello","reference":"hello-token","value":"cHJpdmF0ZQ==","Value":"private"}`, false},
		{"secret_set", `{"app":"hello","reference":"hello-token","value":"cHJpdmF0ZQ==","value":"cHJpdmF0ZQ=="}`, false},
	} {
		t.Run(tc.op+tc.args, func(t *testing.T) {
			raw, _ := json.Marshal(Request{SchemaVersion: 1, Op: tc.op, RequestID: "request", Args: json.RawMessage(tc.args)})
			_, err := DecodeRequest(raw)
			if tc.valid && err != nil || !tc.valid && err == nil {
				t.Fatal("unexpected decoding", err)
			}
			if tc.valid {
				response, _ := NewServer("fixture", nil).Handle(context.Background(), bytes.NewReader(raw))
				if response.OK {
					t.Fatal("mutating operation bypassed authorization")
				}
				b, _ := json.Marshal(response)
				if strings.Contains(string(b), "private") {
					t.Fatal("refusal echoed input")
				}
			}
		})
	}
}
