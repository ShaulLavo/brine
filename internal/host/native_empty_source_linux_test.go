//go:build linux

package host

import (
	"context"
	"strings"
	"testing"

	"github.com/ShaulLavo/brine/internal/store"
	"golang.org/x/sys/unix"
)

func TestNativeEmptySourceRequiresRetainedPreparationAndOwnedCopy(t *testing.T) {
	if _, err := buildNativeEmptySource(context.Background(), &store.Store{}, t.TempDir(), &store.NativeStoppedPreparation{}, nil); err == nil {
		t.Fatal("structural allocation manufactured affirmative empty source")
	}
	if (engineEmptySource{}).unchanged(context.Background()) == nil {
		t.Fatal("unowned source supplied empty-state authority")
	}
}

func TestNativeEmptyCensusRefusesAnyControlProgramHistoryOrUnknownMember(t *testing.T) {
	config := engineTreeMember{Name: "config.toml", Mode: unix.S_IFREG | 0600, Links: 1, Size: 48, SHA256: strings.Repeat("1", 64)}
	seal := engineTreeSeal{members: []engineTreeMember{config}}
	if _, hash, err := nativeEmptyMembers(seal); err != nil || hash != config.SHA256 {
		t.Fatal("actual config-only empty census refused", err)
	}
	for _, name := range []string{"control-db", "control-db/db", "program-bytes", "replicas", "replicas/0/clog/head.stdb.log", "unknown"} {
		member := config
		member.Name = name
		seal.members = []engineTreeMember{config, member}
		if _, _, err := nativeEmptyMembers(seal); err == nil {
			t.Fatal("nonempty or unknown state entered affirmative empty source", name)
		}
	}
	seal.members = []engineTreeMember{config}
	seal.members[0].Links = 2
	if _, _, err := nativeEmptyMembers(seal); err == nil {
		t.Fatal("aliased configuration supplied empty source")
	}
}
