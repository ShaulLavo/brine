package plan

import (
	"encoding/json"
	"testing"

	"github.com/ShaulLavo/brine/internal/policy"
	"github.com/ShaulLavo/brine/internal/spec"
)

func TestManagedRecoveryReferenceChangeUsesOnlyBackupRevision(t *testing.T) {
	in := managedInitializedFixture(t, true)
	before := in.Desired
	raw, err := before.CanonicalBytes()
	if err != nil {
		t.Fatal(err)
	}
	clone := func() policy.Desired {
		var d policy.Desired
		if err := json.Unmarshal(raw, &d); err != nil {
			t.Fatal(err)
		}
		return d
	}
	after := clone()
	after.SpacetimeDB.RecoveryPasswordRef = "new-recovery"
	if !ManagedBackupOnly(after, before) || ManagedBackupOnly(before, before) {
		t.Fatal("recovery reference change not isolated")
	}
	if before.SpacetimeDB.RecoveryPasswordRef == after.SpacetimeDB.RecoveryPasswordRef {
		t.Fatal("classifier mutated the previous desired state")
	}
	for _, mutate := range []func(*policy.Desired){
		func(d *policy.Desired) {
			d.Image = spec.ImageReference("registry.example.com:5000/team/new@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
		},
		func(d *policy.Desired) { d.SpacetimeDB.Resources.MemoryMB++ },
		func(d *policy.Desired) { d.SpacetimeDB.Auth = "new-auth" },
		func(d *policy.Desired) { d.SpacetimeDB.Module.Accepts = []string{"game-v2"} },
		func(d *policy.Desired) { d.SpacetimeDB.BackupDestination = "other" },
	} {
		next := clone()
		next.SpacetimeDB.RecoveryPasswordRef = "new-recovery"
		mutate(&next)
		if ManagedBackupOnly(next, before) {
			t.Fatal("non-backup change entered the backup-only path")
		}
	}
}
