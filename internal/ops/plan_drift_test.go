package ops

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestPlanDriftEventRejectsUnsafePayloads(t *testing.T) {
	for _, raw := range []string{
		`{"paths":["snapshot.identity.private_key"],"changed":1,"truncated":false}`,
		`{"paths":["plan.diff.replica_sync.secret_key"],"changed":1,"truncated":false}`,
		`{"paths":["snapshot.identity.id"],"changed":1,"truncated":null}`,
		`{"paths":["snapshot.identity.id"],"changed":1,"truncated":false,"value":"private"}`,
		`{"paths":["snapshot.identity.id","snapshot.identity.id"],"changed":2,"truncated":false}`,
		`{"paths":[],"changed":0,"truncated":false}`,
	} {
		if ValidateEvent(Event{Kind: "plan_drift", Payload: json.RawMessage(raw)}) == nil {
			t.Fatal("unsafe diagnostic accepted", raw)
		}
	}
	valid := `{"paths":["snapshot.identity.id"],"changed":1,"truncated":false}`
	if err := ValidateEvent(Event{Kind: "plan_drift", Payload: json.RawMessage(valid)}); err != nil {
		t.Fatal(err)
	}
	if err := ValidateEvent(Event{Kind: "plan_drift", Payload: json.RawMessage(strings.Repeat(" ", MaxEventBytes) + valid)}); err == nil {
		t.Fatal("oversized event accepted")
	}
}
