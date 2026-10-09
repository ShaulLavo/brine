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
