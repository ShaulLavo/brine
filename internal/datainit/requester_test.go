package datainit

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestInitializationRequesterAuthorityCannotAliasOperator(t *testing.T) {
	for _, identity := range []string{"local-operator", "operator", "deploy:local-operator", "", "deploy:" + strings.Repeat("a", 63)} {
		if _, err := AgentRequester(identity); err == nil {
			t.Fatalf("agent acquired reserved or invalid authority: %q", identity)
		}
	}
	agent, err := AgentRequester("deploy:" + strings.Repeat("a", 64))
	if err != nil {
		t.Fatal(err)
	}
	if agent.IsLocalOperator() {
		t.Fatal("agent acquired operator authority")
	}
	for _, identity := range []Requester{agent, LocalOperatorRequester()} {
		raw, err := json.Marshal(identity)
		if err != nil {
			t.Fatal(err)
		}
		var reconstructed Requester
		if err = json.Unmarshal(raw, &reconstructed); err != nil || reconstructed != identity || reconstructed.IsLocalOperator() != identity.IsLocalOperator() {
			t.Fatal("durable identity lost authority", err)
		}
	}
}
