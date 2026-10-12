//go:build linux

package host

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func TestNativeBackupResolutionDetectsActualOpenAndDeletedConsumerFD(t *testing.T) {
	workspace := t.TempDir()
	name := filepath.Join(workspace, "owned-source")
	file, err := os.OpenFile(name, os.O_CREATE|os.O_RDWR|os.O_EXCL, 0600)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = file.Close() }()
	process := fmt.Sprintf("/proc/%d", os.Getpid())
	if err = nativeUploadProcessAbsent(context.Background(), process, workspace); err == nil {
		t.Fatal("actual source consumer FD ignored")
	}
	if err = os.Remove(name); err != nil {
		t.Fatal(err)
	}
	if err = nativeUploadProcessAbsent(context.Background(), process, workspace); err == nil {
		t.Fatal("deleted but still open source consumer FD ignored")
	}
	if err = file.Close(); err != nil {
		t.Fatal(err)
	}
	if err = nativeUploadProcessAbsent(context.Background(), process, workspace); err != nil {
		t.Fatal("closed source consumer remained active", err)
	}
}
func TestNativeBackupResolutionWorkspaceContainmentHasComponentBoundaries(t *testing.T) {
	root := "/owned/native-restores/123"
	if !nativeUploadContains(root, root+"/bundle/input") || !nativeUploadContains(root, root+"/archive (deleted)") || nativeUploadContains(root, root+"4/bundle") || nativeUploadContains(root, "relative") {
		t.Fatal("workspace consumer classification crossed ownership")
	}
}
