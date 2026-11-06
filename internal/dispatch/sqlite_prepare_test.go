package dispatch

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/ShaulLavo/brine/internal/jobs"
	"github.com/ShaulLavo/brine/internal/result"
)

func TestSQLitePreparationStrictMutatingRequests(t *testing.T) {
	key := strings.Repeat("a", 32)
	valid := map[string]string{
		"sqlite_prepare":         `{"app":"hello","database":"","supersedes":"","idempotency_key":"` + key + `"}`,
		"sqlite_prepare_abandon": `{"preparation_id":"` + strings.Repeat("b", 32) + `","idempotency_key":"` + key + `"}`,
	}
	for op, raw := range valid {
		if class, ok := ClassOf(op); !ok || class != Mutating {
			t.Fatal("preparation operations must be mutating", op)
		}
		if _, err := operations[op].decode(json.RawMessage(raw)); err != nil {
			t.Fatal("valid request refused", op, err)
		}
		accepted, err := json.Marshal(result.Success("brine host "+op, jobs.Accepted{Status: "accepted", OperationID: "sqlite-operation"}))
		if err != nil {
			t.Fatal(err)
		}
		if _, err = DecodeResponse(accepted, op); err != nil {
			t.Fatal("accepted response refused", op, err)
		}
	}
	for _, raw := range []string{
		`{"app":"hello","database":"","supersedes":"","idempotency_key":"` + key + `","fence":"x"}`,
		`{"app":"hello","database":"../main","supersedes":"","idempotency_key":"` + key + `"}`,
		`{"app":"hello","database":"","supersedes":"latest","idempotency_key":"` + key + `"}`,
		`{"app":"hello","database":"","idempotency_key":"` + key + `"}`,
	} {
		if _, err := decodeSQLitePrepare(json.RawMessage(raw)); err == nil {
			t.Fatal("unsafe prepare request admitted", raw)
		}
	}
	for _, raw := range []string{
		`{"preparation_id":"latest","idempotency_key":"` + key + `"}`,
		`{"preparation_id":"` + strings.Repeat("b", 32) + `","idempotency_key":"` + key + `","start":true}`,
	} {
		if _, err := decodeSQLiteAbandon(json.RawMessage(raw)); err == nil {
			t.Fatal("unsafe abandon request admitted", raw)
		}
	}
}
