//go:build linux

package data

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func archiveDeleteFixture(t *testing.T) (ArchiveTree, AppIncarnationID, ArchiveID, ArchiveDeletionParent) {
	t.Helper()
	tree, inc, id := archiveMoveFixture(t)
	if err := RenameArchiveTree(tree, inc, id); err != nil {
		t.Fatal(err)
	}
	parent, err := CaptureArchiveDeletionParent(tree, inc, id)
	if err != nil {
		t.Fatal(err)
	}
	return tree, inc, id, parent
}

func TestArchiveDeletionRemovesOnlyRecordedBundleAndAdoptsExactAbsence(t *testing.T) {
	tree, inc, id, parent := archiveDeleteFixture(t)
	bundle := filepath.Join(string(tree.Root), tree.ArchiveRelative)
	if err := os.Mkdir(filepath.Join(bundle, "nested"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bundle, "nested", "database.sqlite"), []byte("archived"), 0600); err != nil {
		t.Fatal(err)
	}
	sibling := filepath.Join(filepath.Dir(bundle), "unrelated")
	if err := os.Mkdir(sibling, 0700); err != nil {
		t.Fatal(err)
	}
	if err := DeleteArchiveTree(context.Background(), tree, inc, id, parent, false); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(bundle); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("bundle remains", err)
	}
	if _, err := os.Stat(sibling); err != nil {
		t.Fatal("unrelated archive changed", err)
	}
	if err := DeleteArchiveTree(context.Background(), tree, inc, id, parent, false); err == nil {
		t.Fatal("missing bundle admitted without durable-intent adoption")
	}
	if err := DeleteArchiveTree(context.Background(), tree, inc, id, parent, true); err != nil {
		t.Fatal("exact absence could not be adopted", err)
	}
}

func TestArchiveDeletionRefusesReplacedParentEvenWhenBundleAppearsAbsent(t *testing.T) {
	tree, inc, id, parent := archiveDeleteFixture(t)
	archives := filepath.Join(string(tree.Root), "archives")
	if err := os.Rename(archives, archives+"-old"); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(archives, 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := InspectArchiveDeletionParent(tree, inc, id, parent); err == nil {
		t.Fatal("replacement parent hid undeleted bundle")
	}
	if err := DeleteArchiveTree(context.Background(), tree, inc, id, parent, true); err == nil {
		t.Fatal("replacement namespace adopted as deletion")
	}
	if _, err := os.Stat(filepath.Join(archives+"-old", string(id))); err != nil {
		t.Fatal("original bundle changed", err)
	}
}

func TestArchiveDeletionRejectsUnsafeEntriesBeforeAnyEffects(t *testing.T) {
	for _, kind := range []string{"symlink", "hardlink", "public-file", "public-directory", "fifo"} {
		t.Run(kind, func(t *testing.T) {
			tree, inc, id, parent := archiveDeleteFixture(t)
			bundle := filepath.Join(string(tree.Root), tree.ArchiveRelative)
			good := filepath.Join(bundle, "a-good")
			if err := os.WriteFile(good, []byte("preserved"), 0600); err != nil {
				t.Fatal(err)
			}
			unsafe := filepath.Join(bundle, "z-unsafe")
			switch kind {
			case "symlink":
				if err := os.Symlink(good, unsafe); err != nil {
					t.Fatal(err)
				}
			case "hardlink":
				if err := os.Link(good, unsafe); err != nil {
					t.Fatal(err)
				}
			case "public-file":
				if err := os.WriteFile(unsafe, nil, 0600); err != nil {
					t.Fatal(err)
				}
				if err := os.Chmod(unsafe, 0644); err != nil { // #nosec G302 -- Deliberately unsafe fixture.
					t.Fatal(err)
				}
			case "public-directory":
				if err := os.Mkdir(unsafe, 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.Chmod(unsafe, 0755); err != nil { // #nosec G302 -- Deliberately unsafe fixture.
					t.Fatal(err)
				}
			case "fifo":
				if err := unix.Mkfifo(unsafe, 0600); err != nil {
					t.Fatal(err)
				}
			}
			if err := DeleteArchiveTree(context.Background(), tree, inc, id, parent, false); err == nil {
				t.Fatal("unsafe tree deleted")
			}
			if content, err := os.ReadFile(good); err != nil || string(content) != "preserved" { // #nosec G304 -- Path belongs to the private test fixture.
				t.Fatal("preflight refusal changed bytes", err)
			}
		})
	}
}

func TestArchiveDeletionCancellationPreservesBundleAndPartialDeletionResumes(t *testing.T) {
	tree, inc, id, parent := archiveDeleteFixture(t)
	bundle := filepath.Join(string(tree.Root), tree.ArchiveRelative)
	file := filepath.Join(bundle, "remaining.sqlite")
	if err := os.WriteFile(file, []byte("remaining"), 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := DeleteArchiveTree(ctx, tree, inc, id, parent, false); !errors.Is(err, context.Canceled) {
		t.Fatal("canceled deletion proceeded", err)
	}
	if _, err := os.Stat(file); err != nil {
		t.Fatal("canceled deletion changed bytes", err)
	}
	// Simulate an interruption after a subset of files was removed. The surviving
	// bundle retains its exact recorded inode and can be finished under the intent.
	if err := os.Remove(file); err != nil {
		t.Fatal(err)
	}
	if err := DeleteArchiveTree(context.Background(), tree, inc, id, parent, true); err != nil {
		t.Fatal("partial deletion did not resume", err)
	}
}

func TestArchiveDeletionRefusesReappearedSourceAndReplacedBundle(t *testing.T) {
	for _, conflict := range []string{"source", "bundle"} {
		t.Run(conflict, func(t *testing.T) {
			tree, inc, id, parent := archiveDeleteFixture(t)
			bundle := filepath.Join(string(tree.Root), tree.ArchiveRelative)
			preserved := bundle
			if conflict == "source" {
				if err := os.Mkdir(filepath.Join(string(tree.Root), tree.SourceRelative), 0700); err != nil {
					t.Fatal(err)
				}
			} else {
				preserved = bundle + "-old"
				if err := os.Rename(bundle, preserved); err != nil {
					t.Fatal(err)
				}
				if err := os.Mkdir(bundle, 0700); err != nil {
					t.Fatal(err)
				}
			}
			if err := DeleteArchiveTree(context.Background(), tree, inc, id, parent, true); err == nil {
				t.Fatal("conflicting local identity deleted")
			}
			if _, err := os.Stat(preserved); err != nil {
				t.Fatal("original bundle changed", err)
			}
		})
	}
}

func TestArchiveDeletionRefusesDepthOverflowBeforeAnyEffects(t *testing.T) {
	tree, inc, id, parent := archiveDeleteFixture(t)
	bundle := filepath.Join(string(tree.Root), tree.ArchiveRelative)
	good := filepath.Join(bundle, "a-good")
	if err := os.WriteFile(good, []byte("preserved"), 0600); err != nil {
		t.Fatal(err)
	}
	directory := bundle
	for i := 0; i < 34; i++ {
		directory = filepath.Join(directory, "nested")
		if err := os.Mkdir(directory, 0700); err != nil {
			t.Fatal(err)
		}
	}
	if err := DeleteArchiveTree(context.Background(), tree, inc, id, parent, false); err == nil {
		t.Fatal("unbounded archive traversal admitted")
	}
	if raw, err := os.ReadFile(good); err != nil || string(raw) != "preserved" { // #nosec G304 -- Path belongs to the private test fixture.
		t.Fatal("preflight overflow deleted good file", err)
	}
}

func TestArchiveDeletionRefusesSetIDAndStickyEntryModes(t *testing.T) {
	for _, directory := range []bool{false, true} {
		tree, inc, id, parent := archiveDeleteFixture(t)
		path := filepath.Join(string(tree.Root), tree.ArchiveRelative, "unsafe")
		if directory {
			if err := os.Mkdir(path, 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(path, 0700|os.ModeSticky); err != nil {
				t.Fatal(err)
			} //nolint:gosec // Deliberately unsafe fixture.
		} else {
			if err := os.WriteFile(path, []byte("preserved"), 0600); err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(path, 0600|os.ModeSetgid); err != nil {
				t.Fatal(err)
			} //nolint:gosec // Deliberately unsafe fixture.
		}
		if err := DeleteArchiveTree(context.Background(), tree, inc, id, parent, false); err == nil {
			t.Fatal("special entry mode admitted", directory)
		}
	}
}

func TestArchiveDeletionMountEvidenceRefusesSameDeviceMount(t *testing.T) {
	const original uint64 = 71
	for _, test := range []struct {
		name     string
		stat     unix.Statx_t
		expected uint64
		ok       bool
	}{
		{"original", unix.Statx_t{Mask: unix.STATX_MNT_ID, Mnt_id: original}, original, true},
		{"bind mount", unix.Statx_t{Mask: unix.STATX_MNT_ID, Mnt_id: original + 1}, original, false},
		{"unknown mount", unix.Statx_t{Mnt_id: original}, original, false},
		{"zero mount", unix.Statx_t{Mask: unix.STATX_MNT_ID}, 0, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			if archiveMountEvidenceMatches(test.stat, test.expected) != test.ok {
				t.Fatal("incorrect mount admission")
			}
		})
	}
}

// Isolate every mount in a subprocess namespace: neither a successful test nor
// a failure can mount anything into the host or other tests' namespaces.
func TestArchiveDeletionRefusesBindMountedLiveData(t *testing.T) {
	if os.Getenv("BRINE_ARCHIVE_BIND_TEST") != "1" {
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		binary, err := os.Executable()
		if err != nil {
			t.Fatal(err)
		}
		command := exec.CommandContext(ctx, binary, "-test.run=^TestArchiveDeletionRefusesBindMountedLiveData$", "-test.v") // #nosec G204 -- Only this test binary and fixed test selection execute.
		command.Env = append(os.Environ(), "BRINE_ARCHIVE_BIND_TEST=1")
		command.SysProcAttr = &syscall.SysProcAttr{Cloneflags: unix.CLONE_NEWNS}
		output, err := command.CombinedOutput()
		if errors.Is(err, unix.EPERM) || errors.Is(err, unix.EACCES) {
			t.Skip("private mount namespaces are not permitted")
		}
		if err != nil {
			t.Fatalf("bind-mount regression failed: %s: %v", output, err)
		}
		if strings.Contains(string(output), "--- SKIP: TestArchiveDeletionRefusesBindMountedLiveData") {
			t.Skip(string(output))
		}
		t.Log(string(output))
		return
	}
	currentNamespace, err := os.Stat("/proc/self/ns/mnt")
	if err != nil {
		t.Fatal(err)
	}
	parentNamespace, err := os.Stat(filepath.Join("/proc", strconv.Itoa(os.Getppid()), "ns", "mnt")) // #nosec G304 -- Kernel namespace of this test process's own parent.
	if err != nil || os.SameFile(currentNamespace, parentNamespace) {
		t.Fatal("mount fixture is not isolated from its parent namespace", err)
	}
	if err := unix.Mount("", "/", "", unix.MS_REC|unix.MS_PRIVATE, ""); err != nil {
		if errors.Is(err, unix.EPERM) || errors.Is(err, unix.EACCES) {
			t.Skip("private mounts are not permitted")
		}
		t.Fatal(err)
	}
	tree, inc, id, parent := archiveDeleteFixture(t)
	bundle := filepath.Join(string(tree.Root), tree.ArchiveRelative)
	live := t.TempDir()
	if err := os.Chmod(live, 0700); err != nil { // #nosec G302 -- Private live-data fixture requires traversal.
		t.Fatal(err)
	}
	liveFile := filepath.Join(live, "live.sqlite")
	good := filepath.Join(bundle, "a-good")
	mountpoint := filepath.Join(bundle, "z-mounted-live")
	for _, file := range []string{liveFile, good} {
		if err := os.WriteFile(file, []byte("preserved"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Mkdir(mountpoint, 0700); err != nil {
		t.Fatal(err)
	}
	if err := unix.Mount(live, mountpoint, "", unix.MS_BIND, ""); err != nil {
		if errors.Is(err, unix.EPERM) || errors.Is(err, unix.EACCES) {
			t.Skip("bind mounts are not permitted")
		}
		t.Fatal(err)
	}
	defer func() {
		if err := unix.Unmount(mountpoint, 0); err != nil {
			t.Error(err)
		}
	}()
	// Both paths have the same filesystem device: device checks cannot detect it.
	var original, mounted unix.Stat_t
	if err := unix.Stat(live, &original); err != nil {
		t.Fatal(err)
	}
	if err := unix.Stat(mountpoint, &mounted); err != nil {
		t.Fatal(err)
	}
	if original.Dev != mounted.Dev {
		t.Fatal("fixture is not a same-device bind mount")
	}
	if err := DeleteArchiveTree(context.Background(), tree, inc, id, parent, false); err == nil {
		t.Fatal("bind-mounted live data traversed")
	}
	for _, file := range []string{liveFile, filepath.Join(mountpoint, "live.sqlite"), good} {
		raw, err := os.ReadFile(file) // #nosec G304 -- Private test fixture paths only.
		if err != nil || string(raw) != "preserved" {
			t.Fatal("refused archive purge changed live or archived data", file, err)
		}
	}
}
