package store

import (
	"bytes"
	"context"
	"encoding/json"
	"github.com/ShaulLavo/brine/internal/data"
	"os"
	"path/filepath"
	"time"
)

type NativeBackupInspectionWorkspace struct {
	ID                  string                       `json:"id"`
	OriginalOperationID string                       `json:"original_operation_id"`
	OriginalSHA256      string                       `json:"original_sha256"`
	BindingID           data.ReplicaBindingID        `json:"binding_id"`
	EpochID             data.ReplicaEpochID          `json:"epoch_id"`
	Relative            string                       `json:"relative"`
	Directory           data.EngineDirectoryIdentity `json:"directory"`
	CreatedAt           time.Time                    `json:"created_at"`
}

func (i *NativeBackupInspection) RecordWorkspace(ctx context.Context, parent *os.Root, path string) (string, error) {
	if i.state.readOnly {
		return "", ErrConflict
	}
	permit, err := i.state.ReadEnginePermit(ctx, i.binding)
	if err != nil {
		return "", err
	}
	relative, err := filepath.Rel(string(permit.Reserved.Database.Root), path)
	if err != nil || relative != filepath.Join("native-restores", filepath.Base(relative)) || !data.ValidID(filepath.Base(relative)) {
		return "", ErrConflict
	}
	identity, err := nativeUploadDirectoryIdentity(string(permit.Reserved.Database.Root), relative, parent)
	if err != nil {
		return "", err
	}
	tx, cancel, err := i.state.beginWrite(ctx)
	defer cancel()
	if err != nil {
		return "", err
	}
	defer func() { _ = tx.Rollback() }()
	if err = i.check(ctx, tx); err != nil {
		return "", err
	}
	v := NativeBackupInspectionWorkspace{ID: filepath.Base(relative), OriginalOperationID: i.original.ID, OriginalSHA256: i.originalSHA256, BindingID: i.binding, EpochID: permit.Runtime.EpochID, Relative: relative, Directory: identity, CreatedAt: time.Now().UTC()}
	raw, _ := json.Marshal(v)
	_, err = tx.ExecContext(ctx, "INSERT INTO data_native_backup_inspection_workspaces VALUES(?,?,?)", v.ID, i.original.ID, raw)
	if err != nil {
		return "", err
	}
	return v.ID, tx.Commit()
}
func (i *NativeBackupInspection) Workspaces(ctx context.Context) ([]NativeBackupInspectionWorkspace, error) {
	if err := i.check(ctx, i.state.db); err != nil {
		return nil, err
	}
	rows, err := i.state.db.QueryContext(ctx, "SELECT w.canonical FROM data_native_backup_inspection_workspaces w WHERE w.operation_id=? AND NOT EXISTS(SELECT 1 FROM data_native_backup_inspection_workspace_disposals d WHERE d.workspace_id=w.id)", i.original.ID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	result := make([]NativeBackupInspectionWorkspace, 0)
	for rows.Next() {
		var raw []byte
		var v NativeBackupInspectionWorkspace
		if err = rows.Scan(&raw); err != nil {
			return nil, err
		}
		if len(raw) > 16384 || json.Unmarshal(raw, &v) != nil {
			return nil, &IntegrityError{}
		}
		canonical, _ := json.Marshal(v)
		if !bytes.Equal(raw, canonical) || !data.ValidID(v.ID) || v.OriginalOperationID != i.original.ID || v.OriginalSHA256 != i.originalSHA256 || v.BindingID != i.binding || v.Relative != filepath.Join("native-restores", v.ID) || v.CreatedAt.IsZero() || v.CreatedAt.Location() != time.UTC {
			return nil, &IntegrityError{}
		}
		result = append(result, v)
		if len(result) > 128 {
			return nil, ErrConflict
		}
	}
	return result, rows.Err()
}
func (i *NativeBackupInspection) DisposeWorkspace(ctx context.Context, id string) error {
	if i.state.readOnly || !data.ValidID(id) {
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
	var raw []byte
	var v NativeBackupInspectionWorkspace
	err = tx.QueryRowContext(ctx, "SELECT canonical FROM data_native_backup_inspection_workspaces WHERE id=? AND operation_id=?", id, i.original.ID).Scan(&raw)
	if err != nil || json.Unmarshal(raw, &v) != nil || v.BindingID != i.binding || v.OriginalSHA256 != i.originalSHA256 {
		return ErrConflict
	}
	target, err := i.workspaceRetirementTarget(ctx, tx, id)
	if err != nil {
		return err
	}
	issued, err := i.workspaceRetirementIssued(ctx, tx, target)
	if err != nil || !issued {
		return ErrConflict
	}
	reserved, err := readReservedEngine(ctx, tx, i.binding)
	if err != nil {
		return err
	}
	path := filepath.Join(string(reserved.Database.Root), v.Relative)
	if _, err = os.Lstat(path); !os.IsNotExist(err) {
		return ErrConflict
	}
	proof, _ := json.Marshal(struct {
		WorkspaceID      string    `json:"workspace_id"`
		DescriptorSHA256 string    `json:"descriptor_sha256"`
		At               time.Time `json:"at"`
	}{id, nativePointHash(raw), time.Now().UTC()})
	_, err = tx.ExecContext(ctx, "INSERT INTO data_native_backup_inspection_workspace_disposals VALUES(?,?)", id, proof)
	if err != nil {
		return err
	}
	return tx.Commit()
}
