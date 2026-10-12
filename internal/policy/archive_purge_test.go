package policy

import (
	"bytes"
	"testing"
)

func TestArchivePurgeRequiresAnExplicitSeparateGrant(t *testing.T) {
	omitted, err := Parse(fixture(t))
	if err != nil {
		t.Fatal(err)
	}
	denied, err := Parse(append([]byte("allow_archive_purge=false\n"), fixture(t)...))
	if err != nil {
		t.Fatal(err)
	}
	a, err := omitted.CanonicalBytes()
	if err != nil {
		t.Fatal(err)
	}
	b, err := denied.CanonicalBytes()
	if err != nil {
		t.Fatal(err)
	}
	if (Policy{}).AllowsArchivePurge() || omitted.AllowsArchivePurge() || denied.AllowsArchivePurge() || !bytes.Equal(a, b) || bytes.Contains(a, []byte("allow_archive_purge")) {
		t.Fatal("omitted purge grant changed existing policy or admitted deletion")
	}
	allowed, err := Parse(append([]byte("allow_archive_purge=true\n"), fixture(t)...))
	if err != nil || !allowed.AllowsArchivePurge() || allowed.AllowsLiveRestore() || allowed.Hash() == omitted.Hash() {
		t.Fatal("purge grant missing or combined with live restore", err)
	}
	live, err := Parse(append([]byte("allow_live_restore=true\n"), fixture(t)...))
	if err != nil || live.AllowsArchivePurge() {
		t.Fatal("live restore authorized purge", err)
	}
	for _, prefix := range []string{"allow_archive_purge=1\n", "allow_archive_purge=\"true\"\n", "allow_agent_archive_purge=true\n"} {
		if _, err := Parse(append([]byte(prefix), fixture(t)...)); err == nil {
			t.Fatal("invalid purge grant admitted")
		}
	}
}
