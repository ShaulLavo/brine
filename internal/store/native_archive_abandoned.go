package store

import (
	"context"
	"database/sql"
	"errors"

	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/datapublish"
	"github.com/ShaulLavo/brine/internal/ops"
)

func readNativeArchiveAbandonedRepositories(ctx context.Context, tx *sql.Tx, binding data.ReplicaBindingID) ([]nativeRepositoryIntent, error) {
	rows, err := tx.QueryContext(ctx, "SELECT i.id FROM data_native_repository_intents i LEFT JOIN data_native_epochs e ON e.epoch_id=i.epoch_id AND e.binding_id=i.binding_id WHERE i.binding_id=? AND e.epoch_id IS NULL ORDER BY i.id", binding)
	if err != nil {
		return nil, err
	}
	ids := []string{}
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil || len(ids) == 4096 {
			return nil, errors.Join(ErrConflict, err, rows.Close())
		}
		ids = append(ids, id)
	}
	if err = errors.Join(rows.Err(), rows.Close()); err != nil {
		return nil, err
	}
	intents := make([]nativeRepositoryIntent, len(ids))
	for i, id := range ids {
		intent, err := readNativeRepositoryIntentTx(ctx, tx, id)
		if err != nil || checkNativeArchiveAbandonedRepository(ctx, tx, intent) != nil {
			return nil, errors.Join(ErrConflict, err)
		}
		intents[i] = intent
	}
	return intents, nil
}

func checkNativeArchiveAbandonedRepository(ctx context.Context, tx *sql.Tx, intent nativeRepositoryIntent) error {
	r := intent.record
	original, err := nativeResolutionOperation(ctx, tx, r.OperationID)
	if err != nil || original.Kind != ops.ModulePublishApply || original.State != ops.RecoveryRequired || original.SecretRef != r.PlanID {
		return errors.Join(ErrConflict, err)
	}
	claim, err := readPublicationClaimTx(ctx, tx, r.PlanID)
	if err != nil || claim.RepositoryIntentID != r.ID || claim.ApplyOpID != original.ID || claim.Requester != original.Requester {
		return errors.Join(ErrConflict, err)
	}
	var id string
	err = tx.QueryRowContext(ctx, "SELECT id FROM operations WHERE kind='module_publish_resolve' AND recovery_of=? AND secret_ref=? AND state='succeeded'", original.ID, r.PlanID).Scan(&id)
	if err != nil {
		return errors.Join(ErrConflict, err)
	}
	resolver, err := nativeResolutionOperation(ctx, tx, id)
	if err != nil || resolver.Requester != original.Requester || resolver.App != original.App {
		return errors.Join(ErrConflict, err)
	}
	var raw []byte
	if err = tx.QueryRowContext(ctx, "SELECT canonical FROM operation_outcomes WHERE operation_id=?", id).Scan(&raw); err != nil {
		return err
	}
	outcome, err := ops.DecodeTaskOutcome(resolver, raw)
	receipt, decodeErr := datapublish.DecodeTaskReceipt(outcome.Receipt)
	if err != nil || decodeErr != nil || receipt.Outcome != "source_preserved" || receipt.TransitionState != "not_admitted" || receipt.ApplyOperationID != original.ID || receipt.PlanID != r.PlanID || receipt.PointID != r.PointID || receipt.BindingID != string(r.BindingID) || receipt.OldEpochID != string(r.SourceEpochID) || receipt.NewEpochID != string(r.EpochID) {
		return errors.Join(ErrConflict, err, decodeErr)
	}
	_, hash, err := readPublicationProofTx(ctx, tx, r.PlanID, "source_preservation", "source_preservation")
	if err != nil || hash != receipt.CompletionSHA256 {
		return errors.Join(ErrConflict, err)
	}
	// Incoming repositories can initialize or read config, but cannot upload a
	// snapshot before an engine epoch exists. Keep their reserved prefixes anyway.
	return nil
}
