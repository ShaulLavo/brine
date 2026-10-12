package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"github.com/ShaulLavo/brine/internal/archive"
	"github.com/ShaulLavo/brine/internal/data"
	"strings"
	"time"
)

func (s *Store) claimNativeArchiveDatabase(ctx context.Context, tx *sql.Tx, p archive.Plan, o archive.Operation, d archive.NativeDatabase, guard *data.EngineLock) (data.QuiescenceFence, error) {
	var zero data.QuiescenceFence
	var task string
	if err := tx.QueryRowContext(ctx, "SELECT id FROM operations WHERE kind='native_retire' AND secret_ref=? AND app=? AND requester=? AND state='preflight'", p.ID, p.Candidate.App, p.Requester).Scan(&task); err != nil {
		return zero, err
	}
	var count int
	if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM operations WHERE kind='native_retire' AND secret_ref=? AND app=? AND requester=? AND state='preflight'", p.ID, p.Candidate.App, p.Requester).Scan(&count); err != nil || count != 1 {
		return zero, ErrConflict
	}
	point, err := readNativePreparedPointTx(ctx, tx, nativeArchivePointID(d))
	if err != nil || point.view.BindingID != d.Binding.BindingID || point.view.EpochID != d.Head.EpochID || point.view.RuntimeVersion != d.Head.Version || point.view.RuntimeSHA256 != d.Head.RuntimeSHA256 || p.PolicyHash != "sha256:"+strings.TrimPrefix(point.view.PolicySHA256, "sha256:") {
		return zero, ErrConflict
	}
	consumer := nativePointConsumer{kind: nativePointRetire, pointID: point.view.PointID, planID: p.ID, applyOperationID: task, requester: p.Requester, binding: d.Binding.BindingID, epoch: d.Head.EpochID, generation: p.Generation, generationID: point.view.GenerationID, policySHA256: strings.TrimPrefix(p.PolicyHash, "sha256:")}
	point, err = s.claimNativePreparedPointTx(ctx, tx, guard, consumer)
	if err != nil {
		return zero, err
	}
	raw, _ := consumer.canonical()
	if _, err = tx.ExecContext(ctx, "INSERT INTO data_native_archive_claims VALUES(?,?,?,?,?,?,?)", p.ArchiveID, o.ID, task, d.Binding.BindingID, point.view.PointID, point.view.FenceOwnerOpID, raw); err != nil {
		return zero, err
	}
	for _, ref := range d.Dependencies {
		raw, err := json.Marshal(ref)
		if err != nil {
			return zero, err
		}
		if _, err = tx.ExecContext(ctx, "INSERT INTO data_native_archive_dependencies VALUES(?,?,?,?,?)", p.ArchiveID, d.Binding.BindingID, ref.EpochID, ref.ID, raw); err != nil {
			return zero, err
		}
	}
	for _, tree := range d.TransitionTrees {
		raw, err := json.Marshal(tree)
		if err != nil {
			return zero, err
		}
		if _, err = tx.ExecContext(ctx, "INSERT INTO data_native_archive_trees VALUES(?,?,?,?)", p.ArchiveID, d.Binding.BindingID, tree.TransitionID, raw); err != nil {
			return zero, err
		}
	}
	return data.QuiescenceFence{ID: point.view.FenceID, IncarnationID: d.Binding.IncarnationID, DatabaseID: d.Binding.DatabaseID, OperationID: point.view.FenceOwnerOpID, State: data.FenceHeld}, nil
}

func checkNativeArchiveFenceOwner(ctx context.Context, q dataQuerier, o archive.Operation, f data.QuiescenceFence) error {
	var owner, pointID string
	err := q.QueryRowContext(ctx, "SELECT original_fence_owner,point_id FROM data_native_archive_claims c JOIN data_replica_bindings b ON b.id=c.binding_id WHERE c.archive_operation_id=? AND b.database_id=?", o.ID, f.DatabaseID).Scan(&owner, &pointID)
	if err != nil || owner != f.OperationID {
		return errors.Join(ErrConflict, err)
	}
	var raw []byte
	if err = q.QueryRowContext(ctx, "SELECT canonical FROM data_native_point_consumers WHERE point_id=? AND kind='retire' AND plan_id=?", pointID, o.PlanID).Scan(&raw); err != nil {
		return err
	}
	return nil
}

func checkNativeArchiveCoverageTx(ctx context.Context, tx *sql.Tx, o archive.Operation, p archive.Plan, d archive.NativeDatabase, c archive.NativeCoverage) error {
	point, err := readNativePreparedPointTx(ctx, tx, c.Point.ID)
	if err != nil || point.hash != c.Point.SHA256 || point.view.FenceID != c.FenceID || point.view.FenceOwnerOpID != c.FenceOwnerOpID || point.view.BindingID != d.Binding.BindingID || point.view.RuntimeVersion != d.Head.Version || point.view.RuntimeSHA256 != d.Head.RuntimeSHA256 || point.view.SourceWitnessSHA256 != c.SourceWitnessSHA256 || point.view.CheckpointSHA256 != c.CheckpointSHA256 || point.view.StopProofSHA256 != c.StopProofSHA256 || point.view.RestoreReceiptSHA256 != c.RestoreReceiptSHA256 || !time.Now().UTC().Before(point.view.ExpiresAt) {
		return ErrConflict
	}
	if err = checkNativeArchiveFenceOwner(ctx, tx, o, data.QuiescenceFence{DatabaseID: d.Binding.DatabaseID, OperationID: c.FenceOwnerOpID}); err != nil {
		return err
	}
	_, err = readNativePreparedBundleTx(ctx, tx, point.view.PointID)
	if err != nil {
		return err
	}
	var raw []byte
	if err = tx.QueryRowContext(ctx, "SELECT canonical FROM data_native_archive_verifications WHERE archive_id=? AND point_id=?", p.ArchiveID, point.view.PointID).Scan(&raw); err != nil {
		return err
	}
	var record nativeArchiveVerification
	if json.Unmarshal(raw, &record) != nil || record.Format != "brine-native-archive-verification-v1" || record.ArchiveID != p.ArchiveID || record.PointID != point.view.PointID || record.PointSHA256 != point.hash || record.Coverage != c {
		return ErrConflict
	}
	canonical, _ := json.Marshal(record)
	if string(canonical) != string(raw) {
		return &IntegrityError{}
	}
	return nil
}
