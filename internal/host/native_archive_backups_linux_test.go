//go:build linux

package host

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/ShaulLavo/brine/internal/archive"
	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/localexec"
	"github.com/ShaulLavo/brine/internal/policy"
	"github.com/ShaulLavo/brine/internal/store"
)

type archiveBackupManager struct {
	home       string
	loaded     map[string]bool
	active     map[string]bool
	mutations  []string
	failReload bool
	failStop   bool
	foreign    bool
}

func (r *archiveBackupManager) Execute(_ context.Context, c localexec.Command) (localexec.Result, error) {
	if c.Path != "/usr/bin/systemctl" || len(c.Args) < 2 {
		return localexec.Result{}, archive.ErrRefused
	}
	if c.Mutation {
		return r.mutate(c.Args[1:])
	}
	name := c.Args[2]
	load, active, fragment := "not-found", "inactive", ""
	if r.loaded[name] {
		load, fragment = "loaded", filepath.Join(r.home, managedServiceDirectory, name)
	}
	if r.active[name] {
		active = "active"
	}
	if strings.Contains(c.Args[len(c.Args)-1], "MainPID") {
		return localexec.Result{Stdout: strings.Replace(archiveStopped, "LoadState=loaded", "LoadState="+load, 1)}, nil
	}
	if r.foreign {
		fragment = "/foreign/unit"
	}
	return localexec.Result{Stdout: fmt.Sprintf("LoadState=%s\nActiveState=%s\nFragmentPath=%s\nDropInPaths=\nJob=\n", load, active, fragment)}, nil
}

func (r *archiveBackupManager) mutate(args []string) (localexec.Result, error) {
	r.mutations = append(r.mutations, strings.Join(args, " "))
	switch args[0] {
	case "stop":
		r.active[args[1]] = false
		if r.failStop {
			r.failStop = false
			return localexec.Result{}, context.DeadlineExceeded
		}
	case "daemon-reload":
		if r.failReload {
			r.failReload = false
			return localexec.Result{ExitCode: 1}, nil
		}
		for name := range r.loaded {
			_, err := os.Stat(filepath.Join(r.home, managedServiceDirectory, name))
			r.loaded[name] = err == nil
		}
	default:
		return localexec.Result{}, archive.ErrRefused
	}
	return localexec.Result{}, nil
}

func archiveBackupFixture(t *testing.T) (nativeArchiveBackups, *archiveBackupManager) {
	t.Helper()
	_, home, _ := managedUnitsFixture(t)
	binding := data.ReplicaBindingID(strings.Repeat("a", 32))
	metadata := store.NativeBackupActivationMetadata{Plan: store.NativeBackupRevisionPlan{BindingID: binding, Backup: policy.SpacetimePolicy{CaptureIntervalSeconds: 37}}}
	service, timer := nativeBackupUnitBytes(metadata)
	backup := store.NativeBackupPolicy{BindingID: binding, Backup: metadata.Plan.Backup, UnitSHA256: nativeBackupBytesHash(service), TimerSHA256: nativeBackupBytesHash(timer)}
	name := nativeBackupUnitName(metadata)
	for suffix, raw := range map[string][]byte{".service": service, ".timer": timer} {
		if err := os.WriteFile(filepath.Join(home, managedServiceDirectory, name+suffix), raw, 0600); err != nil {
			t.Fatal(err)
		}
	}
	wants := filepath.Join(home, managedServiceDirectory, "timers.target.wants")
	if err := os.Mkdir(wants, 0755); err != nil { //nolint:gosec // Match the directory created by systemd enablement.
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(home, managedServiceDirectory, name+".timer"), filepath.Join(wants, name+".timer")); err != nil {
		t.Fatal(err)
	}
	r := &archiveBackupManager{home: home, loaded: map[string]bool{name + ".service": true, name + ".timer": true}, active: map[string]bool{name + ".service": true, name + ".timer": true}}
	session, err := localexec.NewSession(r, nativeBackupFixtureUID(t), home, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	b, err := newNativeArchiveBackups(home, session, backup)
	if err != nil {
		t.Fatal(err)
	}
	return b, r
}

func TestNativeArchiveRetiresBackupTimerBeforeServiceAndPreservesOtherBindings(t *testing.T) {
	b, r := archiveBackupFixture(t)
	other := filepath.Join(b.home, managedServiceDirectory, "brine-native-backup-"+strings.Repeat("b", 32)+".timer")
	if err := os.WriteFile(other, []byte("recreated app"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := b.retire(t.Context()); err != nil {
		t.Fatal(err)
	}
	want := []string{"stop " + b.units[0].Name, "stop " + b.units[1].Name, "daemon-reload"}
	if !reflect.DeepEqual(r.mutations, want) {
		t.Fatal("scheduler stop order", r.mutations)
	}
	if present, err := b.inspect(t.Context()); err != nil || present {
		t.Fatal("retired backup still installed or scheduled", present, err)
	}
	if err := b.retire(t.Context()); err != nil {
		t.Fatal("completed removal did not converge", err)
	}
	raw, err := os.ReadFile(other) //nolint:gosec // Owned fixture path.
	if err != nil || string(raw) != "recreated app" {
		t.Fatal("changed another binding", err)
	}
}

func TestNativeArchiveBackupRetirementRecoversInterruptedStopAndRemoval(t *testing.T) {
	for _, afterRemoval := range []bool{false, true} {
		b, r := archiveBackupFixture(t)
		r.failReload, r.failStop = afterRemoval, !afterRemoval
		if err := b.retire(t.Context()); err == nil {
			t.Fatal("unknown effect reported success")
		}
		if err := b.retire(t.Context()); err != nil {
			t.Fatal("interrupted removal did not converge", err)
		}
		if present, err := b.inspect(t.Context()); err != nil || present {
			t.Fatal("recovered timer still scheduled", present, err)
		}
	}
}

func TestNativeArchiveBackupRetirementPreservesForeignUnits(t *testing.T) {
	for _, managerDrift := range []bool{false, true} {
		b, r := archiveBackupFixture(t)
		r.foreign = managerDrift
		path := filepath.Join(b.home, managedServiceDirectory, b.units[1].Name)
		var err error
		if !managerDrift {
			err = os.WriteFile(path, []byte("foreign service"), 0600)
		}
		if err != nil {
			t.Fatal(err)
		}
		if b.retire(t.Context()) == nil || len(r.mutations) != 0 {
			t.Fatal("unowned backup units changed", r.mutations)
		}
		if _, err := os.Stat(path); err != nil {
			t.Fatal("foreign unit removed", err)
		}
	}
}
