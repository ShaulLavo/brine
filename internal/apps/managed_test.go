package apps

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/target"
)

func TestManagedPreparedStatusHasNoServingRelease(t *testing.T) {
	binding := data.ReplicaBindingID(strings.Repeat("a", 32))
	managed := ManagedStatus{BindingID: binding, EpochID: data.ReplicaEpochID(strings.Repeat("b", 32)), State: "unpublished", Prepared: true, Fenced: true, EngineUnit: "brine-engine-" + string(binding) + ".service", EnginePort: target.Known(target.Port(20000)), EngineActive: target.Known(false), EngineLogs: "unavailable"}
	report := Report{Apps: []Status{{App: "game", Managed: &managed, Health: Health{UnitActive: target.Observation[bool]{Status: target.Unknown}, Direct: "not_checked"}, Drift: Drift{State: "unknown", Fields: []string{}}}}}
	raw, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := DecodeReport(raw)
	if err != nil {
		t.Fatal(err)
	}
	if decoded.Apps[0].Current != nil || decoded.Apps[0].Managed == nil || decoded.Apps[0].Managed.EngineActive.Value == nil || *decoded.Apps[0].Managed.EngineActive.Value {
		t.Fatal("prepared status claimed a serving engine/release")
	}
	for _, changed := range []string{
		strings.Replace(string(raw), `"engine_logs":"unavailable"`, `"engine_logs":"retained"`, 1),
		strings.Replace(string(raw), `"engine_active":{"status":"known","value":false}`, `"engine_active":{"status":"known","value":true}`, 1),
		strings.Replace(string(raw), `"state":"unpublished"`, `"state":"other"`, 1),
		strings.Replace(string(raw), `"prepared":true`, `"prepared":true,"publisher_token":"secret"`, 1),
	} {
		if _, err := DecodeReport([]byte(changed)); err == nil {
			t.Fatal("invalid/value-bearing managed status admitted")
		}
	}
}
