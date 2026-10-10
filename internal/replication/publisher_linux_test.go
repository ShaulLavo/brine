//go:build linux

package replication

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func publisherFixture(t *testing.T) (ArtifactPublisher, Artifacts) {
	t.Helper()
	tmp := os.TempDir()
	if info, err := os.Stat("/work/tmp"); err == nil && info.IsDir() {
		tmp = "/work/tmp"
	}
	root, err := os.MkdirTemp(tmp, "br-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(root); err != nil {
			t.Error(err)
		}
	})
	state := filepath.Join(root, "state")
	units := filepath.Join(root, "units")
	for _, p := range []string{state, units} {
		if err := os.Mkdir(p, 0700); err != nil {
			t.Fatal(err)
		}
	}
	b := testBinding()
	b.SocketPath = filepath.Join(state, "replication", b.BindingID, "control.sock")
	// Unix socket paths need to stay below the kernel bound.
	if len(b.SocketPath) > 107 {
		t.Skip("test temporary root exceeds socket bound")
	}
	config, err := RenderConfig(b)
	if err != nil {
		t.Fatal(err)
	}
	name, err := ServiceName(b.BindingID)
	if err != nil {
		t.Fatal(err)
	}
	return ArtifactPublisher{StateRoot: state, UnitRoot: units}, Artifacts{Binding: b, Config: config, Service: []byte("# Brine-owned replica=" + b.BindingID + " config=" + ConfigHash(config) + "\n[Service]\nType=simple\n"), ConfigPath: filepath.Join(state, "replication", b.BindingID, "configs", ConfigHash(config)[7:]+".yml"), LifetimeLock: filepath.Join(state, "replica-locks", b.BindingID+".lock"), ServicePath: filepath.Join(units, name)}
}
func TestPublisherExactIdempotencePrivateModesAndDriftRefusal(t *testing.T) {
	p, a := publisherFixture(t)
	ctx := context.Background()
	if err := p.Publish(ctx, a); err != nil {
		t.Fatal(err)
	}
	before, err := os.Stat(a.LifetimeLock)
	if err != nil {
		t.Fatal(err)
	}
	if err = p.Publish(ctx, a); err != nil {
		t.Fatal("idempotence", err)
	}
	after, err := os.Stat(a.LifetimeLock)
	if err != nil || !os.SameFile(before, after) {
		t.Fatal("lock inode replaced", err)
	}
	for path, want := range map[string][]byte{a.ConfigPath: a.Config, a.ServicePath: a.Service, a.LifetimeLock: {}} {
		// #nosec G304 -- Read only the publisher-generated paths in this test's private fixture.
		got, err := os.ReadFile(path)
		info, statErr := os.Stat(path)
		if err != nil || statErr != nil || string(got) != string(want) || info.Mode().Perm() != 0600 {
			t.Fatal("invalid published file", path, err, statErr)
		}
	}
	if err = os.WriteFile(a.ConfigPath, []byte("foreign"), 0600); err != nil {
		t.Fatal(err)
	}
	if p.Publish(ctx, a) == nil {
		t.Fatal("config drift overwritten")
	}
	got, _ := os.ReadFile(a.ConfigPath)
	if string(got) != "foreign" {
		t.Fatal("drift repaired")
	}
}
func TestPublisherRejectsSymlinksHardlinksModesAndForeignPaths(t *testing.T) {
	for _, kind := range []string{"root-link", "ancestor-link", "file-link", "hardlink", "mode", "foreign-path"} {
		t.Run(kind, func(t *testing.T) {
			p, a := publisherFixture(t)
			if err := p.Publish(context.Background(), a); err != nil {
				t.Fatal(err)
			}
			switch kind {
			case "root-link":
				link := p.StateRoot + "-link"
				if err := os.Symlink(p.StateRoot, link); err != nil {
					t.Fatal(err)
				}
				p.StateRoot = link
			case "ancestor-link":
				dir := filepath.Dir(a.ConfigPath)
				if err := os.Rename(dir, dir+"-original"); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(dir+"-original", dir); err != nil {
					t.Fatal(err)
				}
			case "file-link":
				if err := os.Rename(a.ServicePath, a.ServicePath+"-original"); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(a.ServicePath+"-original", a.ServicePath); err != nil {
					t.Fatal(err)
				}
			case "hardlink":
				if err := os.Link(a.ConfigPath, a.ConfigPath+"-link"); err != nil {
					t.Fatal(err)
				}
			case "mode":
				// #nosec G302 -- Intentionally unsafe fixture file; publication must refuse drift.
				if err := os.Chmod(a.ConfigPath, 0644); err != nil {
					t.Fatal(err)
				}
			case "foreign-path":
				a.LifetimeLock = filepath.Join(p.StateRoot, "foreign.lock")
			}
			if p.Publish(context.Background(), a) == nil {
				t.Fatal("unsafe artifact admitted")
			}
		})
	}
}
func TestPublisherFsyncUnknownPreservesExactRetry(t *testing.T) {
	p, a := publisherFixture(t)
	p.sync = func(int) error { return errors.New("fsync unknown") }
	if p.Publish(context.Background(), a) == nil {
		t.Fatal("unknown fsync reported success")
	}
	p.sync = nil
	if err := p.Publish(context.Background(), a); err != nil {
		t.Fatal("exact retry refused", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if p.Publish(ctx, a) == nil {
		t.Fatal("canceled publisher succeeded")
	}
}
