package dispatch

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/ShaulLavo/brine/internal/result"
)

func TestDeploymentStateProtocolIsReadOnlyAndStrict(t *testing.T) {
	class, ok := ClassOf("deploy_state")
	if !ok || class != ReadOnly {
		t.Fatal("state query can mutate")
	}
	valid := DeploymentState{ReviewHash: "sha256:" + strings.Repeat("a", 64), Databases: []DeploymentDatabase{{Name: "main", Prepared: true, Credentials: true}}}
	wire, err := json.Marshal(result.Success("brine host deploy_state", valid))
	if err != nil {
		t.Fatal(err)
	}
	response, err := DecodeResponse(wire, "deploy_state")
	if err != nil || !response.OK {
		t.Fatal(err)
	}
	for _, raw := range []string{
		`{"review_hash":"bad","databases":[]}`,
		`{"review_hash":"sha256:` + strings.Repeat("a", 64) + `","databases":null}`,
		`{"review_hash":"sha256:` + strings.Repeat("a", 64) + `","databases":[{"name":"main","prepared":false,"initialized":true,"credentials":false,"fenced":false}]}`,
		`{"review_hash":"sha256:` + strings.Repeat("a", 64) + `","databases":[{"name":"main","prepared":true,"initialized":false,"credentials":false,"fenced":false,"secret":"refused"}]}`,
	} {
		if _, err := decodeDeploymentState([]byte(raw)); err == nil {
			t.Fatal("accepted invalid state", raw)
		}
	}
}

func TestDeploymentOperationRecoveryProtocolIsReadOnlyAndStrict(t *testing.T) {
	class, ok := ClassOf("deploy_operation")
	if !ok || class != ReadOnly {
		t.Fatal(class)
	}
	for _, raw := range []string{`{"plan_id":"bad","kind":"deploy","idempotency_key":"key"}`, `{"plan_id":"sha256:` + strings.Repeat("a", 64) + `","kind":"secret_set","idempotency_key":"key"}`} {
		if _, err := decodeDeploymentOperationQuery([]byte(raw)); err == nil {
			t.Fatal("invalid query accepted")
		}
	}
	for _, id := range []string{"", "accepted-op"} {
		wire, err := json.Marshal(result.Success("brine host deploy_operation", DeploymentOperation{OperationID: id}))
		if err != nil {
			t.Fatal(err)
		}
		if _, err = DecodeResponse(wire, "deploy_operation"); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := decodeDeploymentOperation([]byte(`{"operation_id":"job","secret":"refused"}`)); err == nil {
		t.Fatal("extra data admitted")
	}
}
