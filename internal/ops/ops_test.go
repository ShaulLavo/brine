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
