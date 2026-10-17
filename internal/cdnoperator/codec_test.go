package cdnoperator

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/ShaulLavo/brine/internal/cdn"
	"github.com/ShaulLavo/brine/internal/cdn/cdntest"
)

func operatorPlan(t *testing.T) cdn.Plan {
	t.Helper()
	f := cdntest.New(t)
	a := f.Attachment(f.EdgeA.Profile().Reference)
	observation, err := f.EdgeA.Observe(t.Context(), a)
	if err != nil {
		t.Fatal(err)
	}
	steps, err := f.EdgeA.Actions(a, observation)
	if err != nil {
		t.Fatal(err)
	}
	p, err := cdn.NewPlan(cdn.PlanningContext{Attachment: a, PolicyHash: cdn.Hash([]byte("policy")), Initial: []cdn.Observation{observation}}, f.Sample, steps, nil)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestCDNProgressClosedVariantsRoundTrip(t *testing.T) {
	p := operatorPlan(t)
	variants := []cdn.Work{
		cdn.Waiting{Code: "inspect_pending"},
		cdn.Finished{Operation: "op", Succeeded: true, Revision: p.Context.Attachment.Revision},
		cdn.NeedObservation{Request: cdn.PrepareStep{Operation: "op", StepIndex: 0, Attachment: p.Context.Attachment, Step: p.Steps[0]}},
		cdn.NeedObservation{Request: cdn.NeedQualification{Qualification: cdn.Qualification{Attachment: p.Context.Attachment, Sample: p.Sample}}},
	}
	for _, work := range variants {
		before := Progress{Operation: "op", Plan: p, Work: work}
		raw, err := json.Marshal(before)
		if err != nil {
			t.Fatal(err)
		}
		var after Progress
		if err := json.Unmarshal(raw, &after); err != nil || !reflect.DeepEqual(after, before) {
			t.Fatalf("lost typed work %T: %v", work, err)
		}
		bad := strings.Replace(string(raw), `"operation":"op"`, `"operation":"op","operation":"op"`, 1)
		if json.Unmarshal([]byte(bad), &after) == nil {
			t.Fatal("duplicate progress identity accepted")
		}
	}
}

func TestCDNPlanCodecRejectsNestedAliasesDuplicatesAndNulls(t *testing.T) {
	p := operatorPlan(t)
	raw, _ := json.Marshal(p)
	for _, change := range [][2]string{
		{`"hostname":`, `"Hostname":`},
		{`"reference":"public"`, `"reference":"public","reference":"public"`},
		{`"before":"`, `"Before":"`},
		{`"kind":"initial"`, `"kind":"initial","kind":"initial"`},
	} {
		bad := strings.Replace(string(raw), change[0], change[1], 1)
		if bad == string(raw) {
			t.Fatal("test mutation did not apply", change)
		}
		var parsed cdn.Plan
		if DecodeExact([]byte(bad), &parsed) == nil {
			t.Fatal("malformed nested plan accepted", change)
		}
	}
	var request PlanRequest
	if DecodeExact([]byte(`{"app":"hello","binding":"public","profile":"edge","mode":"enabled","revision":"","sample_path":"/objects/a.bin","recover_operation":null}`), &request) == nil {
		t.Fatal("null operation identity accepted")
	}
}

func TestCDNEvidenceCodecRejectsOpenVariantsAndNestedAlias(t *testing.T) {
	valid := `{"operation":"op","result":{"kind":"uncertain","value":{"effect":"effect","code":"unknown"}}}`
	var request ResultRequest
	if json.Unmarshal([]byte(valid), &request) != nil {
		t.Fatal("valid uncertain result rejected")
	}
	for _, bad := range []string{
		strings.Replace(valid, `"uncertain"`, `"execute_url"`, 1),
		strings.Replace(valid, `"effect":`, `"Effect":`, 1),
		strings.Replace(valid, `"code":"unknown"`, `"code":"unknown","code":"unknown"`, 1),
		strings.Replace(valid, `"effect":"effect"`, `"effect":null`, 1),
	} {
		if json.Unmarshal([]byte(bad), &request) == nil {
			t.Fatal("open or malformed result accepted")
		}
	}
}
