package store

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/nativecapture"
)

func (s *Store) NativeBackupStatus(ctx context.Context, app, database string) (nativecapture.BackupStatus, error) {
	binding, err := s.SelectNativeTaskBinding(ctx, app, database)
	if err != nil {
		return nativecapture.BackupStatus{}, err
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nativecapture.BackupStatus{}, err
	}
	defer func() { _ = tx.Rollback() }()
	permit, err := readEnginePermit(ctx, tx, binding)
	if err != nil {
		return nativecapture.BackupStatus{}, err
	}
	policy, _, err := readNativeBackupPolicy(ctx, tx, binding)
	if err != nil {
		return nativecapture.BackupStatus{}, errors.Join(ErrConflict, err)
	}
	material, err := readNativeCurrentRepositoryMaterialRecords(ctx, tx, permit)
	if err != nil {
		return nativecapture.BackupStatus{}, err
	}
	status := nativecapture.BackupStatus{Engine: "spacetimedb", BindingID: string(binding), EpochID: string(permit.Runtime.EpochID), State: "unverified", ModuleSHA256: string(permit.Runtime.Module.Wasm), SchemaSHA256: permit.Runtime.Module.Schema.SHA256, CaptureIntervalSeconds: policy.Backup.CaptureIntervalSeconds, VerificationIntervalSeconds: policy.Backup.VerificationIntervalSeconds, MaintenanceBudgetSeconds: policy.Backup.MaxMaintenanceSeconds, Loss: "unknown", Fenced: nativeHasHeldFence(permit.Fences)}
	if permit.Runtime.State == data.EngineUnpublished {
		status.ModuleSHA256, status.SchemaSHA256 = "", ""
	}
	var newest string
	err = tx.QueryRowContext(ctx, "SELECT id FROM data_native_verified_points WHERE binding_id=? AND epoch_id=? ORDER BY json_extract(canonical,'$.captured_at') DESC LIMIT 1", binding, permit.Runtime.EpochID).Scan(&newest)
	if err == nil {
		bundle, err := readNativeVerifiedPointBundleTx(ctx, tx, newest)
		if err != nil {
			return nativecapture.BackupStatus{}, err
		}
		point := bundle.point.view
		status.PointID, status.PointSHA256, status.CapturedAt, status.VerifiedAt = point.PointID, bundle.point.hash, &point.CapturedAt, &point.VerifiedAt
		status.State = "verified"
		if time.Since(point.VerifiedAt) > time.Duration(policy.Backup.VerificationIntervalSeconds)*time.Second {
			status.State = "degraded"
		}
	} else if !errors.Is(err, sql.ErrNoRows) {
		return nativecapture.BackupStatus{}, err
	}
	expires := material.CredentialRecord.ExpiresAt
	status.CredentialExpired = expires != nil && !time.Now().UTC().Before(*expires)
	var pending int
	err = tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM operations o JOIN data_native_task_inputs i ON i.id=o.secret_ref WHERE i.binding_id=? AND o.kind='native_capture' AND o.state IN ('launch_unknown','preflight','failed','recovery_required')", binding).Scan(&pending)
	if err != nil {
		return nativecapture.BackupStatus{}, err
	}
	if err := status.ObserveCadence(time.Now().UTC(), policy.ActivatedAt); err != nil {
		return nativecapture.BackupStatus{}, &IntegrityError{}
	}
	if status.CredentialExpired || pending > 0 || status.Fenced || status.MissedCaptureIntervals > 0 {
		status.State = "degraded"
	}
	if !status.Valid() {
		return nativecapture.BackupStatus{}, &IntegrityError{}
	}
	return status, tx.Commit()
}
