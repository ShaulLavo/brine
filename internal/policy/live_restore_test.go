package policy

import (
	"bytes"
	"testing"
)

func TestLiveRestoreGrantDefaultsFalseAndPreservesDefaultCanonicalBytes(t *testing.T) {
	if (Policy{}).AllowsLiveRestore() {
		t.Fatal("missing operator policy granted live restore")
	}
	omitted, err := Parse(fixture(t))
	if err != nil {
		t.Fatal(err)
	}
	explicitFalse, err := Parse(append([]byte("allow_live_restore=false\n"), fixture(t)...))
	if err != nil {
		t.Fatal(err)
	}
	a, err := omitted.CanonicalBytes()
	if err != nil {
		t.Fatal(err)
	}
	b, err := explicitFalse.CanonicalBytes()
	if err != nil {
		t.Fatal(err)
	}
	if omitted.AllowsLiveRestore() || explicitFalse.AllowsLiveRestore() || !bytes.Equal(a, b) || omitted.Hash() != explicitFalse.Hash() || bytes.Contains(a, []byte("allow_live_restore")) {
		t.Fatal("default-false grant changed existing canonical policy")
	}
	allowed, err := Parse(append([]byte("allow_live_restore=true\n"), fixture(t)...))
	if err != nil || !allowed.AllowsLiveRestore() || allowed.Hash() == omitted.Hash() {
		t.Fatal("explicit operator grant missing from policy identity", err)
	}
}

func TestLiveRestoreGrantRejectsAliasesAndWrongTypes(t *testing.T) {
	for _, prefix := range []string{"allow_agent_live_restore=true\n", "allow_live_restore=1\n", "allow_live_restore=\"true\"\n", "Allow_Live_Restore=true\n"} {
		if _, err := Parse(append([]byte(prefix), fixture(t)...)); err == nil {
			t.Fatal("invalid policy grant admitted")
		}
	}
}
