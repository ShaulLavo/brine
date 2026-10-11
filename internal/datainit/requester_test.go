package datainit

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestInitializationRequesterIsRestrictedServerIdentity(t *testing.T) {
	for _, identity := range []string{"local-operator", "operator", "deploy:local-operator", "", "deploy:" + strings.Repeat("a", 63)} {
		if _, err := ParseRequester(identity); err == nil {
			t.Fatalf("agent acquired reserved or invalid authority: %q", identity)
		}
	}
	agent, err := ParseRequester("deploy:" + strings.Repeat("a", 64))
	if err != nil {
		t.Fatal(err)
	}
	for _, identity := range []Requester{agent} {
		raw, err := json.Marshal(identity)
		if err != nil {
			t.Fatal(err)
		}
		var reconstructed Requester
		if err = json.Unmarshal(raw, &reconstructed); err != nil || reconstructed != identity {
			t.Fatal("durable identity lost authority", err)
		}
	}
}
