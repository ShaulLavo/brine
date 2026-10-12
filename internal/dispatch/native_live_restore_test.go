package dispatch

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestNativeLiveRestoreReferenceOnlyRequestsAndClasses(t *testing.T) {
	plan := "sha256:" + strings.Repeat("a", 64)
	cases := []struct {
		op    string
		class Class
		args  string
	}{
		{"native_live_restore_plan", Mutating, `{"app":"game","database":"main","selected_point_id":"` + strings.Repeat("b", 32) + `","current_prepared_point_id":"` + strings.Repeat("c", 32) + `","recovery_release_id":"release"}`},
		{"native_live_restore", Mutating, `{"app":"game","plan_id":"` + plan + `","idempotency_key":"` + strings.Repeat("b", 32) + `"}`},
		{"native_live_restore_inspect", ReadOnly, `{"operation_id":"original"}`},
		{"native_live_restore_review_resolution", Mutating, `{"operation_id":"original"}`},
		{"native_live_restore_resolve", Mutating, `{"original_operation_id":"original","reviewed_resolution_plan_id":"` + plan + `","idempotency_key":"` + strings.Repeat("b", 32) + `"}`},
	}
	for _, tc := range cases {
		class, ok := ClassOf(tc.op)
		if !ok || class != tc.class {
			t.Fatal("authority class changed", tc.op)
		}
		request := Request{SchemaVersion: SchemaVersion, Op: tc.op, RequestID: "restore", Args: json.RawMessage(tc.args)}
		if _, err := EncodeRequest(request); err != nil {
			t.Fatal(tc.op, err)
		}
		for _, field := range []string{"source_path", "requester", "epoch_id", "password", "receipt", "observation", "sql"} {
			request.Args = json.RawMessage(strings.Replace(tc.args, `{`, `{"`+field+`":"PLANTED",`, 1))
			if _, err := EncodeRequest(request); err == nil {
				t.Fatal("request admitted authority", tc.op, field)
			}
		}
	}
}
