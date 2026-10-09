package apps

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/ShaulLavo/brine/internal/ops"
	"github.com/ShaulLavo/brine/internal/plan"
	"github.com/ShaulLavo/brine/internal/spec"
	"github.com/ShaulLavo/brine/internal/target"
)

func TestReportWireRejectsMalformedAndValueBearingFields(t *testing.T) {
	digest := "sha256:" + strings.Repeat("a", 64)
	report := Report{Apps: []Status{{App: "hello", Current: Release{ID: "current", PlanID: digest, ImageDigest: digest, Port: 20000, Domains: []spec.Domain{"hello.example.com"}}, Health: Health{UnitActive: target.Known(true), Direct: "healthy"}, Drift: Drift{State: "in_sync", Fields: []string{}}}}}
	encoded, e := json.Marshal(report)
	if e != nil {
		t.Fatal(e)
	}
	if _, e := DecodeReport(encoded); e != nil {
		t.Fatal(e)
	}
	for _, raw := range []string{`null`, `{"apps":null}`, `{"apps":[],"apps":[]}`, `{"Apps":[]}`, string(encoded) + ` {}`, strings.Replace(string(encoded), `"app":"hello"`, `"app":"../escape"`, 1), strings.Replace(string(encoded), `"current":{`, `"current":{"environment":{"TOKEN":"planted"},`, 1), strings.Replace(string(encoded), `"unit_active":{"status":"known","value":true}`, `"unit_active":{"status":"known"}`, 1), strings.Replace(string(encoded), `"port":20000`, `"port":0`, 1)} {
		if _, e := DecodeReport([]byte(raw)); e == nil {
			t.Fatal("accepted", raw)
		}
	}
	report.Apps[0].LastOperation = &ops.Operation{Kind: ops.Deploy, ID: "op1", PlanID: digest, State: ops.State("unknown_state")}
	encoded, _ = json.Marshal(report)
	if _, e := DecodeReport(encoded); e == nil {
		t.Fatal("invalid operation accepted")
	}
}
func TestRollbackWireRejectsLiteralSettings(t *testing.T) {
	p := RollbackPlan{PlanID: "sha256:" + strings.Repeat("a", 64), ReleaseID: "previous", Compatibility: "stateless_compatible", Kind: plan.Update, Diff: &plan.ConfigurationDiff{Secrets: []plan.SecretChange{}}, Conflicts: []plan.Diagnostic{}}
	encoded, _ := json.Marshal(p)
	if _, e := DecodeRollback(encoded); e != nil {
		t.Fatal(e)
	}
	for _, raw := range []string{strings.Replace(string(encoded), `"diff":{`, `"diff":{"environment":{"values":{"TOKEN":"planted"}},`, 1), strings.Replace(string(encoded), `"kind":"update"`, `"kind":"create"`, 1), strings.Replace(string(encoded), `stateless_compatible`, `unknown`, 1), `{"plan_id":null}`, strings.Replace(string(encoded), `"secrets":[]`, `"secrets":[],"secrets":[]`, 1)} {
		if _, e := DecodeRollback([]byte(raw)); e == nil {
			t.Fatal("accepted", raw)
		}
	}
}
