//go:build linux

package host

import "testing"

func TestInspectionCleanupPreservesOnlyExactRetainedUploadWorkspace(t *testing.T) {
	retained := "/owned/native-restores/retained"
	if !inspectionWorkspaceIsRetainedUpload(retained, retained) {
		t.Fatal("issued upload entered inspection cleanup")
	}
	for _, path := range []string{"/owned/native-restores/other", retained + "/bundle", "/foreign/native-restores/retained"} {
		if inspectionWorkspaceIsRetainedUpload(path, retained) {
			t.Fatal("another workspace borrowed upload retention", path)
		}
	}
	if inspectionWorkspaceIsRetainedUpload("", "") || inspectionWorkspaceIsRetainedUpload(retained, "") {
		t.Fatal("absent upload descriptor hid inspection workspace")
	}
}
