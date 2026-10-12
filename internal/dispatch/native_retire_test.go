package dispatch

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestNativeRetireRequestsContainOnlyReviewReferences(t *testing.T) {
	for _, tc := range []struct {
		op, args string
		class    Class
	}{
		{"native_retire_plan", `{"app":"game"}`, Mutating},
		{"native_retire", `{"app":"game","plan_id":"sha256:` + strings.Repeat("a", 64) + `","idempotency_key":"` + strings.Repeat("b", 32) + `"}`, Mutating},
		{"native_retire_inspect", `{"archive_id":"` + strings.Repeat("c", 32) + `"}`, ReadOnly},
		{"native_retire_resolve", `{"operation_id":"` + strings.Repeat("b", 32) + `","idempotency_key":"` + strings.Repeat("c", 32) + `"}`, Mutating},
	} {
		class, ok := ClassOf(tc.op)
		if !ok || class != tc.class {
			t.Fatal("native retirement changed authority class")
		}
		request := Request{SchemaVersion: SchemaVersion, Op: tc.op, RequestID: "retire", Args: json.RawMessage(tc.args)}
		if _, err := EncodeRequest(request); err != nil {
			t.Fatal(tc.op, err)
		}
		for _, field := range []string{"source_path", "requester", "fence_id", "receipt", "credentials", "command", "purge"} {
			request.Args = json.RawMessage(strings.Replace(tc.args, `{`, `{"`+field+`":"PLANTED",`, 1))
			if _, err := EncodeRequest(request); err == nil {
				t.Fatal("retirement admitted authority or deletion", field)
			}
		}
	}
}
