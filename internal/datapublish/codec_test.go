package datapublish

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/ShaulLavo/brine/internal/spacetimedb"
)

func recordObject(t *testing.T, value any) map[string]any {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	var object map[string]any
	if json.Unmarshal(raw, &object) != nil {
		t.Fatal("record decode failed")
	}
	return object
}

func assertPlanRecordRefused(t *testing.T, object map[string]any, plan Plan) {
	t.Helper()
	raw, err := json.Marshal(object)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = DecodePlan(raw, plan.source.schema, plan.candidate.schema); !errors.Is(err, ErrRecord) {
		t.Fatal("foreign/mutable plan record accepted", err)
	}
}

func TestPrivatePlanCodecRejectsSafetyOverridesAndForeignContract(t *testing.T) {
	plan, err := NewPlan(planFixture(t, false))
	if err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(map[string]any){
		func(record map[string]any) { record["safe"] = true },
		func(record map[string]any) { record["version"] = 2 },
		func(record map[string]any) { record["requester"] = "deploy:" + strings.Repeat("b", 64) },
		func(record map[string]any) { record["facts"].(map[string]any)["generation"] = nil },
		func(record map[string]any) { record["review"].(map[string]any)["reviewed"] = true },
		func(record map[string]any) {
			record["preflight"].(map[string]any)["publisher_credential"] = "never-a-reference"
		},
		func(record map[string]any) {
			record["source"].(map[string]any)["captured"].(map[string]any)["coverage_contract"] = "generic-all-ack"
		},
		func(record map[string]any) {
			record["candidate"].(map[string]any)["schema"].(map[string]any)["sha256"] = strings.Repeat("f", 64)
		},
		func(record map[string]any) { record["requirements_sha256"] = strings.Repeat("0", 64) },
	} {
		record := recordObject(t, plan)
		mutate(record)
		assertPlanRecordRefused(t, record, plan)
	}
	raw, err := json.Marshal(plan)
	if err != nil {
		t.Fatal(err)
	}
	duplicate := strings.Replace(string(raw), `"version":1`, `"version":1,"version":1`, 1)
	if _, err = DecodePlan([]byte(duplicate), plan.source.schema, plan.candidate.schema); !errors.Is(err, ErrRecord) {
		t.Fatal("duplicate plan field accepted", err)
	}
}

func TestEmptyServerRecordRejectsNativePlaceholdersAndMixedArms(t *testing.T) {
	plan, err := NewPlan(planFixture(t, true))
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"native_database", "program_wire_hash", "replica_id", "selector", "coverage_contract"} {
		record := recordObject(t, plan)
		record["source"].(map[string]any)["empty"].(map[string]any)[field] = "invented"
		assertPlanRecordRefused(t, record, plan)
	}
	record := recordObject(t, plan)
	record["source"].(map[string]any)["captured"] = map[string]any{}
	assertPlanRecordRefused(t, record, plan)
	raw, err := json.Marshal(plan)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = DecodePlan(raw, nativeSchema(t, "v1"), plan.candidate.schema); !errors.Is(err, ErrRecord) {
		t.Fatal("fresh arm acquired source native schema", err)
	}
}

func TestCursorCodecCannotInventTransitionOrNewAttempt(t *testing.T) {
	ready := readyToPublish(t, false)
	pending, err := ready.Intend(PublishStage)
	if err != nil {
		t.Fatal(err)
	}
	unknown, err := pending.MarkUnknown()
	if err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(map[string]any){
		func(record map[string]any) { record["state"] = "ready" },
		func(record map[string]any) { record["next"] = "final_transition" },
		func(record map[string]any) { record["scope"].(map[string]any)["apply_op_id"] = "second-attempt" },
		func(record map[string]any) { record["scope"].(map[string]any)["preparation_op_id"] = "rewritten-owner" },
		func(record map[string]any) {
			record["scope"].(map[string]any)["new_epoch_id"] = unknown.plan.facts.EpochID
		},
		func(record map[string]any) { record["events"].([]any)[0].(map[string]any)["republish"] = true },
	} {
		record := recordObject(t, unknown)
		mutate(record)
		assertCursorRecordRefused(t, record, unknown.plan)
	}
}

func assertCursorRecordRefused(t *testing.T, record map[string]any, plan Plan) {
	t.Helper()
	raw, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = DecodeCursor(raw, plan, recordSHA256(raw)); !errors.Is(err, ErrRecord) {
		t.Fatal("cursor invented continuation", err)
	}
}

func TestCursorCodecCannotRewindTheRetainedRecordHead(t *testing.T) {
	ready := readyToPublish(t, false)
	pending, err := ready.Intend(PublishStage)
	if err != nil {
		t.Fatal(err)
	}
	unknown, err := pending.MarkUnknown()
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(unknown)
	if err != nil {
		t.Fatal(err)
	}
	retainedSHA := recordSHA256(raw)
	if _, err = DecodeCursor(raw, unknown.plan, retainedSHA); err != nil {
		t.Fatal("exact retained record refused", err)
	}
	var record cursorRecord
	if err = json.Unmarshal(raw, &record); err != nil {
		t.Fatal(err)
	}
	record.Events = record.Events[:4]
	record.State = Ready
	record.Next = PublishStage
	truncated, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = DecodeCursor(truncated, unknown.plan, retainedSHA); !errors.Is(err, ErrRecord) {
		t.Fatal("coherent truncation erased a retained publication intent", err)
	}
	for _, anchor := range []string{"", "not-a-hash", strings.Repeat("0", 64)} {
		if _, err = DecodeCursor(raw, unknown.plan, anchor); !errors.Is(err, ErrRecord) {
			t.Fatal("missing or changed retained-record head accepted", err)
		}
	}
}

func TestDestructiveCandidateCannotBecomeRecordByReviewLabel(t *testing.T) {
	spec := planFixture(t, false)
	candidate, err := NewCandidate("2002870aaf07f3381defb11a0ecd975264f4bb348d06e7bd39cca0f69d66d3b6", strings.Repeat("f", 64), nativeSchema(t, "destructive"))
	if err != nil {
		t.Fatal(err)
	}
	spec.Candidate = candidate
	spec.Review = reviewFixture(t, candidate, false)
	if _, err = NewPlan(spec); !errors.Is(err, ErrRecord) {
		t.Fatal("review reference downgraded destructive schema", err)
	}
	if _, err = NewCandidate(strings.Repeat("a", 64), strings.Repeat("b", 64), spacetimedb.CanonicalSchema{}); !errors.Is(err, ErrRecord) {
		t.Fatal("empty schema record accepted", err)
	}
}
