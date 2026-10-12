package store

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"time"

	"github.com/ShaulLavo/brine/internal/data"
)

type nativeWorkspaceRetirement struct {
	Stage            string                       `json:"stage"`
	DescriptorSHA256 string                       `json:"descriptor_sha256"`
	Directory        data.EngineDirectoryIdentity `json:"directory"`
	At               time.Time                    `json:"at"`
}
type nativeWorkspaceRetirementTarget struct {
	stage, relative, hash string
	directory             data.EngineDirectoryIdentity
	created               time.Time
}

func (i *NativeBackupInspection) workspaceRetirementTarget(ctx context.Context, q dataQuerier, id string) (nativeWorkspaceRetirementTarget, error) {
	var raw []byte
	var workspace NativeBackupInspectionWorkspace
	err := q.QueryRowContext(ctx, "SELECT canonical FROM data_native_backup_inspection_workspaces WHERE id=? AND operation_id=?", id, i.original.ID).Scan(&raw)
	if err != nil || json.Unmarshal(raw, &workspace) != nil {
		return nativeWorkspaceRetirementTarget{}, ErrConflict
	}
	canonical, _ := json.Marshal(workspace)
	if !bytes.Equal(raw, canonical) || workspace.ID != id || workspace.BindingID != i.binding || workspace.OriginalOperationID != i.original.ID || workspace.OriginalSHA256 != i.originalSHA256 || workspace.Relative != filepath.Join("native-restores", id) || !data.ValidID(id) {
		return nativeWorkspaceRetirementTarget{}, ErrConflict
	}
	return nativeWorkspaceRetirementTarget{stage: "workspace_retire:" + id, relative: workspace.Relative, hash: nativePointHash(raw), directory: workspace.Directory, created: workspace.CreatedAt}, nil
}
func (i *NativeBackupInspection) uploadRetirementTarget(ctx context.Context, q dataQuerier) (nativeWorkspaceRetirementTarget, error) {
	retained, err := i.Upload(ctx)
	if err != nil {
		return nativeWorkspaceRetirementTarget{}, err
	}
	var raw []byte
	var descriptor nativeUploadDescriptor
	if err = q.QueryRowContext(ctx, "SELECT canonical FROM data_native_upload_descriptors WHERE operation_id=?", i.original.ID).Scan(&raw); err != nil || json.Unmarshal(raw, &descriptor) != nil {
		return nativeWorkspaceRetirementTarget{}, ErrConflict
	}
	canonical, _ := json.Marshal(descriptor)
	if !bytes.Equal(raw, canonical) || descriptor.Directory != retained.Directory || filepath.Base(descriptor.Workspace) != filepath.Base(retained.Workspace) {
		return nativeWorkspaceRetirementTarget{}, ErrConflict
	}
	return nativeWorkspaceRetirementTarget{stage: "upload_retire", relative: descriptor.Workspace, hash: nativePointHash(raw), directory: descriptor.Directory, created: descriptor.IssuedAt}, nil
}
func (i *NativeBackupInspection) beginWorkspaceRetirement(ctx context.Context, target nativeWorkspaceRetirementTarget, parent *os.Root) error {
	if i.state.readOnly {
		return ErrConflict
	}
	reserved, err := i.state.ReadReservedEngine(ctx, i.binding)
	if err != nil {
		return err
	}
	current, err := nativeUploadDirectoryIdentity(string(reserved.Database.Root), target.relative, parent)
	if err != nil || current != target.directory {
		return ErrConflict
	}
	tx, cancel, err := i.state.beginWrite(ctx)
	defer cancel()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if err = i.check(ctx, tx); err != nil {
		return err
	}
	found, err := i.workspaceRetirementIssued(ctx, tx, target)
	if err != nil {
		return err
	}
	if found {
		return tx.Commit()
	}
	record := nativeWorkspaceRetirement{Stage: target.stage, DescriptorSHA256: target.hash, Directory: target.directory, At: time.Now().UTC()}
	if record.At.Before(target.created) {
		return ErrConflict
	}
	raw, _ := json.Marshal(record)
	if _, err = tx.ExecContext(ctx, "INSERT INTO data_native_backup_resolution_events VALUES(?,?,?)", i.original.ID, target.stage, raw); err != nil {
		return err
	}
	return tx.Commit()
}
func (i *NativeBackupInspection) workspaceRetirementIssued(ctx context.Context, q dataQuerier, target nativeWorkspaceRetirementTarget) (bool, error) {
	var raw []byte
	err := q.QueryRowContext(ctx, "SELECT canonical FROM data_native_backup_resolution_events WHERE operation_id=? AND stage=?", i.original.ID, target.stage).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	var record nativeWorkspaceRetirement
	if err != nil || json.Unmarshal(raw, &record) != nil {
		return false, ErrConflict
	}
	canonical, _ := json.Marshal(record)
	if !bytes.Equal(raw, canonical) || record.Stage != target.stage || record.DescriptorSHA256 != target.hash || record.Directory != target.directory || record.At.IsZero() || record.At.Location() != time.UTC || record.At.Before(target.created) {
		return false, ErrConflict
	}
	return true, nil
}

// BeginWorkspaceRetirement journals the held inode after actual absent observation.
// The intent owns crash recovery; it is not a public consumer-absence proof.
func (i *NativeBackupInspection) BeginWorkspaceRetirement(ctx context.Context, id string, parent *os.Root) error {
	if err := i.check(ctx, i.state.db); err != nil {
		return err
	}
	target, err := i.workspaceRetirementTarget(ctx, i.state.db, id)
	if err != nil {
		return err
	}
	return i.beginWorkspaceRetirement(ctx, target, parent)
}
func (i *NativeBackupInspection) WorkspaceRetirementIssued(ctx context.Context, id string) (bool, error) {
	if err := i.check(ctx, i.state.db); err != nil {
		return false, err
	}
	target, err := i.workspaceRetirementTarget(ctx, i.state.db, id)
	if err != nil {
		return false, err
	}
	return i.workspaceRetirementIssued(ctx, i.state.db, target)
}
func (i *NativeBackupInspection) BeginUploadRetirement(ctx context.Context, parent *os.Root) error {
	if err := i.check(ctx, i.state.db); err != nil {
		return err
	}
	target, err := i.uploadRetirementTarget(ctx, i.state.db)
	if err != nil {
		return err
	}
	return i.beginWorkspaceRetirement(ctx, target, parent)
}
func (i *NativeBackupInspection) UploadRetirementIssued(ctx context.Context) (bool, error) {
	if err := i.check(ctx, i.state.db); err != nil {
		return false, err
	}
	target, err := i.uploadRetirementTarget(ctx, i.state.db)
	if err != nil {
		return false, err
	}
	return i.workspaceRetirementIssued(ctx, i.state.db, target)
}
