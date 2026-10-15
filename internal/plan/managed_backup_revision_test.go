package plan

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/ShaulLavo/brine/internal/policy"
	"github.com/ShaulLavo/brine/internal/spec"
	"github.com/ShaulLavo/brine/internal/target"
)

func TestManagedPolicyEditRequiresBackupReactivation(t *testing.T) {
	in := managedInitializedFixture(t, true)
	before := in.Desired
	after := before
	after.PolicyHash = "sha256:" + strings.Repeat("f", 64)
	if !ManagedBackupOnly(after, before) {
		t.Fatal("policy-only edit cannot enter backup reactivation")
	}
	after.SpacetimeDB = nil
	if ManagedBackupOnly(after, before) {
		t.Fatal("engine removal entered backup reactivation")
	}
}

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

func TestManagedLifecycleUsesActivatedBackupDesiredHash(t *testing.T) {
	in := managedInitializedFixture(t, false)
	created, err := Build(in)
	if err != nil || created.Kind == Conflict {
		t.Fatal(err, created.Conflicts)
	}
	in.State.Releases = []CurrentRelease{{App: created.App, ID: "current", Desired: in.Desired, Image: created.Image, HostPort: created.HostPort, Secrets: []SecretBinding{}, Units: []target.Unit{{Name: "brine-engine-" + string(created.Managed.Database.BindingID) + ".service", Hash: "sha256:" + created.Managed.EngineUnitHash}}, CaddyFile: target.CaddyFile{Name: created.App + ".caddy", Hash: "sha256:" + strings.Repeat("a", 64)}, Managed: created.Managed}}
	in.Desired.PolicyHash = "sha256:" + strings.Repeat("f", 64)
	p, err := BuildLifecycle(in, StartApp)
	if err != nil || p.Kind != Conflict || !hasManagedConflict(p, ManagedBackupRevisionRequired) {
		t.Fatal("policy edit admitted lifecycle before activation", p.Conflicts, err)
	}
	raw, err := in.Desired.CanonicalBytes()
	if err != nil {
		t.Fatal(err)
	}
	in.Managed.BackupDesiredHash = hash(raw)
	p, err = BuildLifecycle(in, StartApp)
	if err != nil || p.Kind == Conflict {
		t.Fatal("activated backup policy refused lifecycle", p.Conflicts, err)
	}
	in.Desired.PolicyHash = "sha256:" + strings.Repeat("e", 64)
	p, err = BuildLifecycle(in, StartApp)
	if err != nil || p.Kind != Conflict {
		t.Fatal("later policy edit admitted lifecycle without reactivation", p.Conflicts, err)
	}
}
