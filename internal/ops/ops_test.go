package ops

import "testing"

func TestEventBoundary(t *testing.T) {
	for _, p := range []string{`{"outcome":"intent","outcome":"unknown"}`, `{"Outcome":"intent"}`, `{"outcome":"failed"}`, `{"outcome":"intent","value":"private"}`, `{"outcome":"intent"} {}`} {
		if ValidateEvent(Event{Kind: "launch", Payload: []byte(p)}) == nil {
			t.Errorf("accepted invalid payload %s", p)
		}
	}
	for _, p := range []string{`{"outcome":"intent"}`, `{"outcome":"completed"}`, `{"outcome":"unknown"}`} {
		if e := ValidateEvent(Event{Kind: "launch", Payload: []byte(p)}); e != nil {
			t.Fatal(e)
		}
	}
}

func TestVerifiedNoOpAndKnownFailedCommitTransitions(t *testing.T) {
	for _, pair := range [][2]State{{Preflight, Succeeded}, {Committing, RollingBack}} {
		if !CanTransition(pair[0], pair[1]) {
			t.Errorf("required transition %s -> %s", pair[0], pair[1])
		}
	}
	for _, pair := range [][2]State{{Queued, Succeeded}, {Preparing, Succeeded}, {Starting, Committing}, {Succeeded, RollingBack}} {
		if CanTransition(pair[0], pair[1]) {
			t.Errorf("illegal skip %s -> %s", pair[0], pair[1])
		}
	}
}

func TestCompletedCompatibilityProofCodes(t *testing.T) {
	for _, code := range []string{"stateless_compatible", "compatibility_verified"} {
		payload := func(step, outcome string) []byte {
			return []byte(`{"step":"` + step + `","outcome":"` + outcome + `","code":"` + code + `"}`)
		}
		if e := ValidateEvent(Event{Kind: "step", Payload: payload("check_compatibility", "completed")}); e != nil {
			t.Errorf("compatibility proof refused: %v", e)
		}
		for _, p := range []struct{ step, outcome string }{{"check_direct", "completed"}, {"check_compatibility", "intent"}, {"check_compatibility", "failed"}, {"check_compatibility", "unknown"}} {
			if ValidateEvent(Event{Kind: "step", Payload: payload(p.step, p.outcome)}) == nil {
				t.Errorf("proof code accepted for %s/%s", p.step, p.outcome)
			}
		}
	}
	if ValidateEvent(Event{Kind: "step", Payload: []byte(`{"step":"check_compatibility","outcome":"completed","code":"health_failed"}`)}) == nil {
		t.Fatal("failure code accepted as compatibility proof")
	}
}

func TestOnlyStateEventsReportState(t *testing.T) {
	for _, event := range []Event{{Kind: "launch", State: Succeeded, Payload: []byte(`{"outcome":"intent"}`)}, {Kind: "failure", State: Succeeded, Payload: []byte(`{"code":"interrupted"}`)}, {Kind: "step", State: Succeeded, Payload: []byte(`{"step":"start_unit","outcome":"intent"}`)}} {
		if ValidateEvent(event) == nil {
			t.Errorf("non-state event reported success: %s", event.Kind)
		}
	}
}

func TestLockUnavailableFailureCode(t *testing.T) {
	if err := ValidateEvent(Event{Kind: "failure", Payload: []byte(`{"code":"lock_unavailable"}`)}); err != nil {
		t.Fatalf("lock failure code refused: %v", err)
	}
	if ValidateEvent(Event{Kind: "failure", State: Failed, Payload: []byte(`{"code":"lock_unavailable"}`)}) == nil {
		t.Fatal("failure event supplied authoritative state")
	}
}
