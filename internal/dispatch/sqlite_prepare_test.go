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

func TestSQLitePrepareStrictOptionalSelectors(t *testing.T) {
	base := `"app":"hello","database":"main","supersedes":"","idempotency_key":"` + strings.Repeat("a", 32) + `"`
	plan := `"declaration_plan_id":"sha256:` + strings.Repeat("b", 64) + `"`
	valid := "{" + base + "," + plan + `,"delivered_credential_version":9223372036854775807}`
	if _, err := decodeSQLitePrepare(json.RawMessage(valid)); err != nil {
		t.Fatal(err)
	}
	for _, suffix := range []string{
		plan,
		`"delivered_credential_version":1`,
		plan + `,"delivered_credential_version":null`,
		plan + `,"delivered_credential_version":0`,
		plan + `,"delivered_credential_version":-1`,
		plan + `,"delivered_credential_version":1.5`,
		plan + `,"delivered_credential_version":1e0`,
		plan + `,"delivered_credential_version":"1"`,
		plan + `,"delivered_credential_version":9223372036854775808`,
		plan + `,"delivered_credential_version":18446744073709551616`,
		plan + `,"delivered_credential_version":1,"delivered_credential_version":2`,
		`"declaration_plan_id":null,"delivered_credential_version":1`,
		`"declaration_plan_id":"latest","delivered_credential_version":1`,
	} {
		if _, err := decodeSQLitePrepare(json.RawMessage("{" + base + "," + suffix + "}")); err == nil {
			t.Fatal("accepted invalid selector", suffix)
		}
	}
	abandon := `{"preparation_id":"` + strings.Repeat("b", 32) + `","idempotency_key":"` + strings.Repeat("a", 32) + `","declaration_plan_id":""}`
	if _, err := decodeSQLiteAbandon(json.RawMessage(abandon)); err == nil {
		t.Fatal("abandon accepted selector key")
	}
}
