package policy

import (
	"strings"
	"testing"
	"time"

	"github.com/ShaulLavo/brine/internal/data"
)

func TestBackupCadenceDefaultsAndOverrides(t *testing.T) {
	p, err := Parse(fixture(t))
	if err != nil {
		t.Fatal(err)
	}
	if got := p.Backup(); got != data.DefaultBackupCadence() {
		t.Fatalf("default cadence: %+v", got)
	}
	p, err = Parse(append(fixture(t), []byte(`
[backup]
min_sync_interval="20s"
max_sync_interval="30m"
snapshot_interval="12h"
min_snapshot_interval="2h"
max_snapshot_interval="18h"
`)...))
	if err != nil {
		t.Fatal(err)
	}
	got := p.Backup()
	if got.MinSyncInterval != 20*time.Second || got.MaxSyncInterval != 30*time.Minute || got.SnapshotInterval != 12*time.Hour || got.MinSnapshotInterval != 2*time.Hour || got.MaxSnapshotInterval != 18*time.Hour {
		t.Fatalf("effective cadence: %+v", got)
	}
}

func TestBackupCadenceRejectsInvalidPolicy(t *testing.T) {
	for _, text := range []string{
		`min_sync_interval="0s"`, `max_sync_interval="not-a-duration"`,
		"min_sync_interval=\"2h\"\nmax_sync_interval=\"1h\"",
		"min_snapshot_interval=\"12h\"\nsnapshot_interval=\"6h\"",
		`snapshot_interval="25h"`, `unknown_interval="1m"`,
	} {
		t.Run(text, func(t *testing.T) {
			if _, err := Parse(append(fixture(t), []byte("\n[backup]\n"+text+"\n")...)); err == nil {
				t.Fatal("invalid policy accepted")
			}
		})
	}
}

func TestPersistentPolicyRootsCannotOverlap(t *testing.T) {
	input := strings.Replace(string(fixture(t)), `persistent_roots = ["/srv/brine/data"]`, `persistent_roots = ["/srv/brine/data", "/srv/brine/data/apps"]`, 1)
	if _, err := Parse([]byte(input)); err == nil {
		t.Fatal("overlapping roots admitted")
	}
}

func TestExplicitEmptyBackupDurationsRefuse(t *testing.T) {
	for _, field := range []string{"min_sync_interval", "max_sync_interval", "snapshot_interval", "min_snapshot_interval", "max_snapshot_interval"} {
		t.Run(field, func(t *testing.T) {
			if _, err := Parse(append(fixture(t), []byte("\n[backup]\n"+field+"=\"\"\n")...)); err == nil {
				t.Fatal("explicit empty duration became default")
			}
		})
	}
}

func TestBackupBoundsOnlyNarrowAbsoluteLimits(t *testing.T) {
	for _, text := range []string{`min_sync_interval="9s"`, `max_sync_interval="61m"`, `min_snapshot_interval="59m"`, `max_snapshot_interval="25h"`} {
		t.Run(text, func(t *testing.T) {
			if _, err := Parse(append(fixture(t), []byte("\n[backup]\n"+text+"\n")...)); err == nil {
				t.Fatal("operator widened absolute cadence bounds")
			}
		})
	}
}
