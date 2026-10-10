package ops

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/ShaulLavo/brine/internal/plan"
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

type driftJournal struct{ events []Event }

func (j *driftJournal) AppendEvent(_ context.Context, _ string, e Event) (uint64, error) {
	if err := ValidateEvent(e); err != nil {
		return 0, err
	}
	j.events = append(j.events, e)
	return uint64(len(j.events)), nil
}

func TestRecordPlanDriftObservedArrays(t *testing.T) {
	for _, tc := range []struct{ field, before, after, path string }{
		{"used_ports", "10000", "10001", "snapshot.used_ports.value[0]"},
		{"apps", `{"name":"before"}`, `{"name":"after"}`, "snapshot.apps.value[0].name"},
		{"port_owners", `{"process":"before"}`, `{"process":"after"}`, "snapshot.port_owners.value[0].process"},
		{"persistent_data", `{"fenced":false}`, `{"fenced":true}`, "snapshot.persistent_data.value[0].fenced"},
	} {
		t.Run(tc.field, func(t *testing.T) {
			input := func(identity, value string) plan.Plan {
				raw := []byte(fmt.Sprintf(`{"snapshot":{"identity":{"id":%q},%q:{"status":"known","value":[%s]}}}`, identity, tc.field, value))
				p, err := (plan.Plan{Hash: fmt.Sprintf("sha256:%x", sha256.Sum256(raw))}).WithDecisionInput(raw)
				if err != nil {
					t.Fatal(err)
				}
				return p
			}
			journal := &driftJournal{}
			if err := RecordPlanDrift(context.Background(), journal, "operation", input("before", tc.before), input("after", tc.after)); err != nil {
				t.Fatal(err)
			}
			if len(journal.events) != 1 || journal.events[0].Kind != "plan_drift" {
				t.Fatal("missing diagnostic", journal.events)
			}
			var drift plan.DecisionDrift
			if err := json.Unmarshal(journal.events[0].Payload, &drift); err != nil || !drift.Valid() || !slices.Contains(drift.Paths, tc.path) || !slices.Contains(drift.Paths, "snapshot.identity.id") {
				t.Fatalf("invalid observed-array event %+v %v", drift, err)
			}
		})
	}
}
