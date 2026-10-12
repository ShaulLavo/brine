//go:build linux

package host

import (
	"testing"
)

func TestNativeBackupResolutionWorkspaceContainmentHasComponentBoundaries(t *testing.T) {
	root := "/owned/native-restores/123"
	if !nativeUploadContains(root, root+"/bundle/input") || !nativeUploadContains(root, root+"/archive (deleted)") || nativeUploadContains(root, root+"4/bundle") || nativeUploadContains(root, "relative") {
		t.Fatal("workspace consumer classification crossed ownership")
	}
}
