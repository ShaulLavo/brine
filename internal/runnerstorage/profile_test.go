package runnerstorage

import (
	"strings"
	"testing"
)

const infoFixture = `{"store":{"graphDriverName":"overlay","graphStatus":{"Supports shifting":"true"},"graphOptions":{"overlay.mount_program":{"Executable":"/usr/bin/fuse-overlayfs","Package":"fuse-overlayfs_1.14-1+b1_arm64","Version":"fuse-overlayfs: version 1.13-dev"}}}}`

func TestStorageAdmissionRequiresDynamicMappingAndFixedHelper(t *testing.T) {
	if ValidateInfo([]byte(infoFixture)) != nil {
		t.Fatal("qualified FUSE driver refused")
	}
	for _, raw := range []string{
		strings.Replace(infoFixture, `"Supports shifting":"true"`, `"Supports shifting":"false"`, 1),
		strings.Replace(infoFixture, `"overlay.mount_program":`, `"caller.mount_program":`, 1),
		strings.Replace(infoFixture, FusePath, "/caller/fuse", 1),
		strings.Replace(infoFixture, "1.13-dev", "9.0", 1),
		strings.Replace(infoFixture, `"overlay","graphStatus"`, `"vfs","graphStatus"`, 1),
		strings.Replace(infoFixture, `"overlay.mount_program":`, `"overlay.additionalimagestore":"/borrowed","overlay.mount_program":`, 1),
		`{}`, `null`, infoFixture + infoFixture,
	} {
		if ValidateInfo([]byte(raw)) == nil {
			t.Fatal("unqualified storage admitted")
		}
	}
}
func TestStorageVersionChecksExactQualifiedLine(t *testing.T) {
	if ValidateVersion("fusermount3 version: 3.17.2\nfuse-overlayfs: version 1.13-dev\n") != nil {
		t.Fatal("actual qualified Debian helper refused")
	}
	for _, raw := range []string{"caller fuse-overlayfs: version 1.13-dev", "fuse-overlayfs: version 1.13-dev-unreviewed", "fuse-overlayfs: version 1.14", "", strings.Repeat("x", 4097)} {
		if ValidateVersion(raw) == nil {
			t.Fatal("unqualified helper admitted")
		}
	}
}
