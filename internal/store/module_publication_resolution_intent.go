package store

import (
	"context"
	"database/sql"
	"errors"

	"github.com/ShaulLavo/brine/internal/ops"
)

func checkPublicationResolutionIntentTx(ctx context.Context, tx *sql.Tx, intent ops.Intent, requester string) error {
	source, err := scanOperation(tx.QueryRowContext(ctx, "SELECT id,COALESCE(plan_id,''),requester,idempotency_key,state,created_at,updated_at,kind,app,secret_ref,recovery_of FROM operations WHERE id=?", intent.RecoveryOf))
	if err != nil || source.Kind != ops.ModulePublishApply || source.State != ops.RecoveryRequired || source.App != intent.App || source.SecretRef != intent.SecretRef || source.Requester != requester {
		return errors.Join(ErrConflict, err)
	}
	claim, err := readPublicationClaimTx(ctx, tx, intent.SecretRef)
	if err != nil || claim.ApplyOpID != source.ID || claim.Requester != requester {
		return errors.Join(ErrConflict, err)
	}
	var raw []byte
	if err = tx.QueryRowContext(ctx, "SELECT canonical FROM operation_outcomes WHERE operation_id=?", source.ID).Scan(&raw); err != nil {
		return err
	}
	if _, err = ops.DecodeTaskOutcome(source, raw); err != nil {
		return &IntegrityError{}
	}
	var pending int
	err = tx.QueryRowContext(ctx, "SELECT count(*) FROM operations WHERE kind=? AND recovery_of=? AND state NOT IN ('failed','recovery_required')", ops.ModulePublishResolve, source.ID).Scan(&pending)
	if err != nil || pending != 0 {
		return errors.Join(ErrConflict, err)
	}
	return nil
}
