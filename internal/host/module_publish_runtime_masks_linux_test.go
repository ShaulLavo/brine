//go:build linux

package host

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/unix"
)

const acpiRuntimeMaskFixture = "11 1 0:11 / /proc/acpi ro,relatime - tmpfs tmpfs rw,size=0k\n"
const kcoreRuntimeMaskFixture = "12 1 0:12 /null /proc/kcore ro,nosuid,relatime - devtmpfs udev rw\n"

func TestModuleRuntimeMaskShapesMatchActualAMDProfile(t *testing.T) {
	for _, test := range []struct {
		line string
		kind moduleRuntimeMaskKind
	}{{acpiRuntimeMaskFixture, moduleRuntimeMaskEmpty}, {kcoreRuntimeMaskFixture, moduleRuntimeMaskNull}} {
		before, after, found := strings.Cut(test.line, " - ")
		if !found || moduleRuntimeMaskShape(strings.Fields(before), strings.Fields(after)) != test.kind {
			t.Fatal("actual readonly runtime mask shape refused")
		}
	}
}

func TestModuleRuntimeMaskRejectsForeignOrWritableMounts(t *testing.T) {
	for _, line := range []string{
		strings.Replace(acpiRuntimeMaskFixture, "ro,relatime", "rw,relatime", 1),
		strings.Replace(acpiRuntimeMaskFixture, "ro,relatime", "ro,rw,relatime", 1),
		strings.Replace(acpiRuntimeMaskFixture, "/proc/acpi", "/proc/scsi", 1),
		strings.Replace(acpiRuntimeMaskFixture, " / /proc/acpi", " /source /proc/acpi", 1),
		strings.Replace(acpiRuntimeMaskFixture, " - tmpfs tmpfs ", " - ext4 /dev/source ", 1),
		strings.Replace(kcoreRuntimeMaskFixture, "/null", "/sensitive", 1),
		strings.Replace(kcoreRuntimeMaskFixture, "ro,nosuid", "ro", 1),
		strings.Replace(kcoreRuntimeMaskFixture, "ro,nosuid", "rw,nosuid", 1),
		strings.Replace(kcoreRuntimeMaskFixture, "/proc/kcore", "/proc/secret", 1),
		strings.Replace(kcoreRuntimeMaskFixture, "devtmpfs udev", "tmpfs tmpfs", 1),
		"12 1 0:12 /null /proc/kcore ro,nosuid - devtmpfs\n",
	} {
		before, after, _ := strings.Cut(line, " - ")
		if moduleRuntimeMaskShape(strings.Fields(before), strings.Fields(after)) != moduleRuntimeMaskNone {
			t.Fatal("foreign or writable runtime mask shape admitted")
		}
		assertModuleProfilesRefuseMask(t, line)
	}
}

func assertModuleProfilesRefuseMask(t *testing.T, line string) {
	t.Helper()
	if moduleDescriptorMounts([]byte(descriptorMountFixture() + line)) {
		t.Fatal("descriptor profile admitted unsafe runtime mask")
	}
	stage := strings.Replace(descriptorMountFixture(), "2 1 0:2 / /input/module.wasm ro - ext4 artifact ro\n", "", 1) +
		"8 1 0:8 / /data rw - ext4 copy rw\n9 1 0:9 / /keys/private.pem ro - ext4 key ro\n10 1 0:10 / /keys/public.pem ro - ext4 key ro\n"
	if moduleStageMounts([]byte(stage + line)) {
		t.Fatal("publication profile admitted unsafe runtime mask")
	}
}

func TestModuleRuntimeMasksRequireExactActualNodeTypes(t *testing.T) {
	null := unix.Stat_t{Mode: unix.S_IFCHR | 0666, Uid: 65534, Gid: 65534, Nlink: 1, Rdev: unix.Mkdev(1, 3)}
	directory := unix.Stat_t{Mode: unix.S_IFDIR | 01777, Uid: 0, Gid: 0, Nlink: 2, Size: 40}
	if !moduleRuntimeMaskStat(moduleRuntimeMaskNull, null) || !moduleRuntimeMaskStat(moduleRuntimeMaskEmpty, directory) {
		t.Fatal("actual mapped null or empty tmpfs directory metadata refused")
	}
	for _, mutate := range []func(*unix.Stat_t){
		func(s *unix.Stat_t) { s.Mode = unix.S_IFBLK | 0666 },
		func(s *unix.Stat_t) { s.Mode = unix.S_IFREG | 0666 },
		func(s *unix.Stat_t) { s.Mode = unix.S_IFLNK | 0777 },
		func(s *unix.Stat_t) { s.Mode = unix.S_IFCHR | 0777 },
		func(s *unix.Stat_t) { s.Uid = 1000 },
		func(s *unix.Stat_t) { s.Gid = 1000 },
		func(s *unix.Stat_t) { s.Nlink = 2 },
		func(s *unix.Stat_t) { s.Size = 1 },
		func(s *unix.Stat_t) { s.Rdev = unix.Mkdev(1, 5) },
		func(s *unix.Stat_t) { s.Rdev = unix.Mkdev(8, 3) },
	} {
		changed := null
		mutate(&changed)
		if moduleRuntimeMaskStat(moduleRuntimeMaskNull, changed) {
			t.Fatal("unsafe node acquired null-mask authority")
		}
	}
	for _, mode := range []uint32{unix.S_IFLNK | 0777, unix.S_IFREG | 01777, unix.S_IFDIR | 0777} {
		directory.Mode = mode
		if moduleRuntimeMaskStat(moduleRuntimeMaskEmpty, directory) {
			t.Fatal("wrong directory or symlink metadata admitted")
		}
	}
}

func TestModuleRuntimeDirectoryMaskRequiresActualEmptyDirectory(t *testing.T) {
	directory := t.TempDir()
	if !checkRuntimeMaskDirectory(t, directory) {
		t.Fatal("actual empty directory refused")
	}
	path := filepath.Join(directory, "entry")
	if err := os.WriteFile(path, []byte("retained"), 0600); err != nil {
		t.Fatal(err)
	}
	if checkRuntimeMaskDirectory(t, directory) || checkRuntimeMaskDirectory(t, path) || checkRuntimeMaskDirectory(t, filepath.Join(directory, "missing")) {
		t.Fatal("nonempty, regular or missing directory mask admitted")
	}
}

func checkRuntimeMaskDirectory(t *testing.T, path string) bool {
	t.Helper()
	directory, err := os.Open(path) //nolint:gosec // Paths are owned temporary test fixtures.
	if err != nil {
		return false
	}
	defer func() { _ = directory.Close() }()
	return moduleRuntimeMaskDirectoryEmpty(directory)
}
