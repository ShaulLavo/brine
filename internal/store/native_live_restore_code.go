package store

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"github.com/ShaulLavo/brine/internal/liverestore"
)

type nativeRestoreCodeIntent struct {
	Format              string `json:"format"`
	OperationID         string `json:"operation_id"`
	PlanID              string `json:"plan_id"`
	SourceReleaseID     string `json:"source_release_id"`
	SourceReleaseHash   string `json:"source_release_hash"`
	RecoveryReleaseID   string `json:"recovery_release_id"`
	RecoveryReleaseHash string `json:"recovery_release_hash"`
}

func recordNativeRestoreCodeIntentTx(ctx context.Context, tx *sql.Tx, p liverestore.Plan, operation string) error {
	var currentID string
	if err := tx.QueryRowContext(ctx, "SELECT current_id FROM release_heads WHERE app=?", p.Spec().Facts.App).Scan(&currentID); err != nil {
		return err
	}
	source, err := readRelease(ctx, tx, p.Spec().Facts.App, currentID)
	if err != nil || source.Managed == nil || source.Managed.BindingID != p.Spec().Facts.Head.BindingID {
		return errors.Join(ErrConflict, err)
	}
	_, sourceRaw, err := normalizeRelease(source)
	if err != nil {
		return err
	}
	intent := nativeRestoreCodeIntent{Format: "brine-native-restore-code-intent-v1", OperationID: operation, PlanID: p.ID(), SourceReleaseID: source.ID, SourceReleaseHash: nativePointHash(sourceRaw), RecoveryReleaseID: p.Spec().Recovery.ReleaseID, RecoveryReleaseHash: p.Spec().Recovery.CodeSHA256}
	raw, err := json.Marshal(intent)
	if err != nil {
		return ErrInvalid
	}
	_, err = tx.ExecContext(ctx, "INSERT INTO data_native_live_restore_code_intents VALUES(?,?)", operation, raw)
	return err
}

func readNativeRestoreCodeIntent(ctx context.Context, q dataQuerier, p liverestore.Plan) (nativeRestoreCodeIntent, error) {
	var raw []byte
	var operation string
	err := q.QueryRowContext(ctx, "SELECT c.operation_id,i.canonical FROM data_native_live_restore_claims c JOIN data_native_live_restore_code_intents i ON i.operation_id=c.operation_id WHERE c.plan_id=?", p.ID()).Scan(&operation, &raw)
	var intent nativeRestoreCodeIntent
	if err != nil {
		return intent, err
	}
	if len(raw) > 4096 || json.Unmarshal(raw, &intent) != nil || intent.Format != "brine-native-restore-code-intent-v1" || intent.OperationID != operation || intent.PlanID != p.ID() || intent.RecoveryReleaseID != p.Spec().Recovery.ReleaseID || intent.RecoveryReleaseHash != p.Spec().Recovery.CodeSHA256 {
		return intent, &IntegrityError{}
	}
	canonical, err := json.Marshal(intent)
	if err != nil || !bytes.Equal(raw, canonical) {
		return intent, &IntegrityError{}
	}
	source, err := readRelease(ctx, q, p.Spec().Facts.App, intent.SourceReleaseID)
	_, sourceRaw, marshalErr := normalizeRelease(source)
	if err != nil || marshalErr != nil || source.Managed == nil || source.Managed.BindingID != p.Spec().Facts.Head.BindingID || nativePointHash(sourceRaw) != intent.SourceReleaseHash {
		return intent, errors.Join(ErrConflict, err, marshalErr)
	}
	var currentID string
	err = q.QueryRowContext(ctx, "SELECT current_id FROM release_heads WHERE app=?", p.Spec().Facts.App).Scan(&currentID)
	if err != nil || currentID != intent.SourceReleaseID && currentID != intent.RecoveryReleaseID {
		return intent, errors.Join(ErrConflict, err)
	}
	return intent, nil
}

func settleNativeRestoreCodeTx(ctx context.Context, tx *sql.Tx, retained nativeLiveRestoreAttempt) error {
	intent, err := readNativeRestoreCodeIntent(ctx, tx, retained.plan)
	if err != nil {
		return err
	}
	var current string
	if err = tx.QueryRowContext(ctx, "SELECT current_id FROM release_heads WHERE app=?", retained.plan.Spec().Facts.App).Scan(&current); err != nil {
		return err
	}
	if current == intent.RecoveryReleaseID {
		return nil
	}
	result, err := tx.ExecContext(ctx, "UPDATE release_heads SET previous_id=current_id,current_id=? WHERE app=? AND current_id=?", intent.RecoveryReleaseID, retained.plan.Spec().Facts.App, intent.SourceReleaseID)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil || n != 1 {
		return errors.Join(ErrConflict, err)
	}
	return nil
}
