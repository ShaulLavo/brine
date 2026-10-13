//go:build linux

package host

import (
	"context"
	"os"
	"path/filepath"
	"strings"

	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/store"
)

func (o nativeBackupResolutionOperations) cleanupOriginalUpload(ctx context.Context, v *nativeBackupObserved) error {
	workspaceID, err := retainedUploadInspectionWorkspace(ctx, v.inspection, v.retained.Workspace)
	if err != nil {
		return err
	}
	if err := o.disposeOriginalUploadReaders(ctx, v.retained.Workspace); err != nil {
		return err
	}
	if v.originalParent == nil {
		if _, err := os.Lstat(v.retained.Workspace); !os.IsNotExist(err) {
			return errEngineSource
		}
		issued, err := v.inspection.UploadRetirementIssued(ctx)
		if err != nil || !issued {
			return errEngineSource
		}
		if workspaceID != "" {
			return v.inspection.DisposeWorkspace(ctx, workspaceID)
		}
		return nil
	}
	if err := observeNativeWorkspaceAbsent(ctx, v.originalParent); err != nil {
		return err
	}
	if err := v.inspection.BeginUploadRetirement(ctx, v.originalParent); err != nil {
		return err
	}
	if workspaceID != "" {
		if err := v.inspection.BeginWorkspaceRetirement(ctx, workspaceID, v.originalParent); err != nil {
			return err
		}
	}
	err = discardNativeWorkspace(v.originalParent, v.retained.Workspace)
	if err == nil {
		v.originalParent = nil
		if workspaceID != "" {
			return v.inspection.DisposeWorkspace(ctx, workspaceID)
		}
	}
	return err
}

func retainedUploadInspectionWorkspace(ctx context.Context, i *store.NativeBackupInspection, path string) (string, error) {
	workspaces, err := i.Workspaces(ctx)
	if err != nil {
		return "", err
	}
	for _, workspace := range workspaces {
		if filepath.Base(path) == workspace.ID {
			return workspace.ID, nil
		}
	}
	return "", nil
}

func (o nativeBackupResolutionOperations) disposeOriginalUploadReaders(ctx context.Context, workspace string) error {
	containers, err := listEngineContainers(ctx, o.runtime.session)
	if err != nil {
		return err
	}
	for _, container := range containers {
		if !nativeUploadContainerUses(container, workspace) {
			continue
		}
		if err = o.disposeOriginalUploadReader(ctx, container, workspace); err != nil {
			return err
		}
	}
	remaining, err := listEngineContainers(ctx, o.runtime.session)
	if err != nil {
		return err
	}
	for _, container := range remaining {
		if nativeUploadContainerUses(container, workspace) {
			return errEngineSource
		}
	}
	return nil
}
func nativeUploadContainerUses(container engineContainer, workspace string) bool {
	for _, mount := range container.Mounts {
		if nativeUploadContains(workspace, mount.Source) {
			return true
		}
	}
	return false
}
func (o nativeBackupResolutionOperations) disposeOriginalUploadReader(ctx context.Context, container engineContainer, workspace string) error {
	nonce := container.Config.Labels["BRINE_PRIVATE_VERIFY"]
	if !data.ValidID(nonce) || container.Name != "brine-native-read-"+nonce || container.State.Running == nil || *container.State.Running || container.State.PID == nil || *container.State.PID != 0 {
		return errEngineSource
	}
	pin, err := readNativeReaderPin(ctx)
	if err != nil {
		return err
	}
	var source string
	for _, mount := range container.Mounts {
		if nativeUploadContains(workspace, mount.Source) && mount.Destination == "/copy/replica" {
			if source != "" {
				return errEngineSource
			}
			source = mount.Source
		}
	}
	if source == "" {
		return errEngineSource
	}
	// This fixed metadata checker verifies the original image, command, security,
	// owner label and exact read-copy mount. It does not manufacture reader proof.
	view := &nativeReadView{nonce: nonce, path: filepath.Dir(source), replica: filepath.Base(source)}
	if !stoppedNativeReader(container, pin, view) {
		return errEngineSource
	}
	_, err = o.runtime.session.Execute(ctx, "/usr/bin/podman", []string{"container", "rm", container.ID}, nil, true)
	return err
}
func nativeUploadContains(workspace, path string) bool {
	clean := strings.TrimSuffix(path, " (deleted)")
	if !filepath.IsAbs(clean) {
		return false
	}
	return clean == workspace || strings.HasPrefix(clean, workspace+string(filepath.Separator))
}

func (o nativeBackupResolutionOperations) cleanupInspectionWorkspaces(ctx context.Context, i *store.NativeBackupInspection, permit store.EnginePermit) error {
	workspaces, err := i.Workspaces(ctx)
	if err != nil {
		return err
	}
	uploadWorkspace, err := i.RetainedUploadWorkspace(ctx)
	if err != nil {
		return err
	}
	for _, workspace := range workspaces {
		if workspace.EpochID != permit.Runtime.EpochID {
			return errEngineSource
		}
		path := filepath.Join(string(permit.Reserved.Database.Root), workspace.Relative)
		if path == uploadWorkspace {
			continue
		}
		if err = o.disposeOriginalUploadReaders(ctx, path); err != nil {
			return err
		}
		if _, err = os.Lstat(path); os.IsNotExist(err) {
			issued, err := i.WorkspaceRetirementIssued(ctx, workspace.ID)
			if err != nil || !issued {
				return errEngineSource
			}
			if err = i.DisposeWorkspace(ctx, workspace.ID); err != nil {
				return err
			}
			continue
		} else if err != nil {
			return errEngineSource
		}
		parent, err := engineOpenRoot(path)
		if err != nil {
			return err
		}
		identity, err := nativeRestoreDirectoryIdentity(parent, ".")
		if err != nil || identity.Device != workspace.Directory.Device || identity.Inode != workspace.Directory.Inode {
			_ = parent.Close()
			return errEngineSource
		}
		err = observeNativeWorkspaceAbsent(ctx, parent)
		if err == nil {
			err = i.BeginWorkspaceRetirement(ctx, workspace.ID, parent)
		}
		if err == nil {
			err = discardNativeWorkspace(parent, path)
		}
		if err != nil {
			_ = parent.Close()
			return err
		}
		if err = i.DisposeWorkspace(ctx, workspace.ID); err != nil {
			return err
		}
	}
	return nil
}
