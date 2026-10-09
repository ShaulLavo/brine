package ops

import (
	"encoding/json"
	"testing"
)

func TestSecretVersionEvent(t *testing.T) {
	for _, name := range []string{"brine.hello.hello-token.v1", "brine.hello.ref_with-parts.v42", "brine.hello.ref.v0", "brine.hello.ref.v01", "brine.hello.ref.v18446744073709551616", "other-hello-ref-v1", "brine--ref-v1", "brine.hello.ref.v1\nPRIVATE"} {
		valid := name == "brine.hello.hello-token.v1" || name == "brine.hello.ref_with-parts.v42"
		b, _ := json.Marshal(SecretVersionPayload{Name: name, Outcome: "intent"})
		if (ValidateEvent(Event{Kind: "secret_version", Payload: b}) == nil) != valid {
			t.Fatalf("name %q", name)
		}
	}
	for _, b := range []string{`{"name":"brine.hello.ref.v1","outcome":"intent","value":"PRIVATE"}`, `{"name":"brine.hello.ref.v1","outcome":"failed"}`, `{"Name":"brine.hello.ref.v1","outcome":"intent"}`} {
		if ValidateEvent(Event{Kind: "secret_version", Payload: json.RawMessage(b)}) == nil {
			t.Fatal("accepted malformed event")
		}
	}
}
