package plan

import (
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ShaulLavo/brine/internal/policy"
)

func cadencePlanFixture(t *testing.T) Input {
	t.Helper()
	in := persistentReady(t)
	existing := installed(t)
	in.Snapshot.Apps = existing.Snapshot.Apps
	in.Snapshot.UsedPorts = existing.Snapshot.UsedPorts
	in.Snapshot.PortOwners = existing.Snapshot.PortOwners
	in.Snapshot.CaddyConfig = existing.Snapshot.CaddyConfig
	in.Snapshot.LiveCaddyFiles = existing.Snapshot.LiveCaddyFiles
	release := existing.State.Releases[0]
	release.Desired = in.Desired
	release.Image = in.Image
	in.State.Releases = []CurrentRelease{release}
	return in
}

func TestCadenceOnlyPlanPreservesAppAndAllowsFencedStorage(t *testing.T) {
	for _, change := range []string{"sync", "snapshot", "snapshot_policy", "both", "fenced"} {
		t.Run(change, func(t *testing.T) {
			in := cadencePlanFixture(t)
			before := in.State.Releases[0].Desired
			in.Desired.Databases = slices.Clone(in.Desired.Databases)
			backup := *in.Desired.Backup
			in.Desired.Backup = &backup
			if change != "snapshot" && change != "snapshot_policy" {
				in.Desired.Databases[0].SyncInterval = 2 * time.Minute
			}
			if change == "snapshot" || change == "snapshot_policy" || change == "both" {
				in.Desired.Backup.SnapshotInterval = 12 * time.Hour
			}
			if change == "snapshot_policy" {
				in.Desired.PolicyHash = "sha256:" + strings.Repeat("f", 64)
				in.Desired.PolicyVersion = "new-policy"
				(*in.Snapshot.PersistentData.Value)[0].Credentials.Value.PolicyHash = in.Desired.PolicyHash
			}
			if change == "fenced" {
				(*in.Snapshot.PersistentData.Value)[0].Fenced = true
			}
			p := build(t, in)
			if p.Kind != Update || p.Lifecycle != ReviseReplica || len(p.Changes) != 1 || p.Changes[0].Kind != ReviseReplica || p.Diff == nil || len(p.DataMounts) != 1 {
				t.Fatal("cadence restarted app or was hidden", p.Kind, p.Lifecycle, p.Conflicts, p.Changes, p.Diff)
			}
			if p.HostPort != in.State.Releases[0].HostPort || !reflect.DeepEqual(in.State.Releases[0].Desired, before) {
				t.Fatal("planning mutated committed app state")
			}
			if p.Diff.Image != nil || p.Diff.Environment != nil {
				t.Fatal("cadence became an app diff")
			}
		})
	}
	in := cadencePlanFixture(t)
	if p := build(t, in); p.Kind != NoOp || p.Lifecycle != "" || len(p.Changes) != 0 {
		t.Fatal("unchanged cadence not no-op", p.Conflicts)
	}
}

func TestCadenceMixedWithAppChangeStillUsesDeployment(t *testing.T) {
	in := cadencePlanFixture(t)
	in.Desired.Databases = slices.Clone(in.Desired.Databases)
	in.Desired.Databases[0].SyncInterval = 2 * time.Minute
	in.Desired.Environment = []policy.Environment{{Name: "APP_ENV", Value: "updated"}}
	p := build(t, in)
	if p.Kind != Update || p.Lifecycle != "" {
		t.Fatal("mixed change bypassed application deployment", p.Conflicts)
	}
	(*in.Snapshot.PersistentData.Value)[0].Fenced = true
	if p = build(t, in); p.Kind != Conflict {
		t.Fatal("mixed app change bypassed held fence")
	}
}
