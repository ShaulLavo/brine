package store

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"github.com/ShaulLavo/brine/internal/ops"
)

type previousResolutionClaim struct {
	OperationSHA256 string `json:"operation_sha256"`
	OutcomeSHA256   string `json:"outcome_sha256"`
	ClaimSHA256     string `json:"claim_sha256"`
}

// PreviousResolutionClaimsSHA256 binds failed claimed attempts to this original
// producer. Only the currently checked opaque claimant can exclude itself.
func (i *NativeBackupInspection) PreviousResolutionClaimsSHA256(ctx context.Context) (string, error) {
	if i == nil || i.state == nil {
		return "", ErrConflict
	}
	if err := i.RequireInitialStagesSettled(ctx); err != nil {
		return "", err
	}
	tx, err := i.state.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return "", err
	}
	defer func() { _ = tx.Rollback() }()
	if err := i.check(ctx, tx); err != nil {
		return "", err
	}
	rows, err := tx.QueryContext(ctx, "SELECT id FROM operations WHERE recovery_of=? ORDER BY id", i.original.ID)
	if err != nil {
		return "", err
	}
	ids := make([]string, 0)
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			break
		}
		ids = append(ids, id)
		if len(ids) > 128 {
			err = ErrConflict
			break
		}
	}
	if err = errors.Join(err, rows.Err(), rows.Close()); err != nil {
		return "", err
	}
	claims := make([]previousResolutionClaim, 0, len(ids))
	for _, id := range ids {
		if id == i.resolver {
			continue
		}
		claim, err := i.previousResolutionClaim(ctx, tx, id)
		if err != nil {
			return "", err
		}
		claims = append(claims, claim)
	}
	raw, err := json.Marshal(claims)
	if err != nil {
		return "", err
	}
	return nativePointHash(raw), tx.Commit()
}

func (i *NativeBackupInspection) previousResolutionClaim(ctx context.Context, q dataQuerier, id string) (previousResolutionClaim, error) {
	var zero previousResolutionClaim
	operation, err := nativeResolutionOperation(ctx, q, id)
	if err != nil || operation.Kind != ops.NativeBackupResolve || operation.RecoveryOf != i.original.ID || operation.Requester != i.original.Requester || operation.App != i.original.App || operation.State != ops.Failed && operation.State != ops.RecoveryRequired {
		return zero, errors.Join(ErrConflict, err)
	}
	var outcomeRaw, claimRaw []byte
	if err := q.QueryRowContext(ctx, "SELECT canonical FROM operation_outcomes WHERE operation_id=?", id).Scan(&outcomeRaw); err != nil {
		return zero, errors.Join(ErrConflict, err)
	}
	outcome, err := ops.DecodeTaskOutcome(operation, outcomeRaw)
	if err != nil || outcome.Error == "" || len(outcome.Receipt) != 0 {
		return zero, errors.Join(ErrConflict, err)
	}
	review, err := readNativeBackupResolutionReview(ctx, q, operation.SecretRef)
	if err != nil || review.OriginalOperationID != i.original.ID || review.OriginalKind != string(i.original.Kind) || review.Requester != i.original.Requester || review.BindingID != string(i.binding) || review.OriginalSHA256 != i.originalSHA256 {
		return zero, errors.Join(ErrConflict, err)
	}
	var eventCount int
	err = q.QueryRowContext(ctx, "SELECT count(*) FROM data_native_backup_resolution_events WHERE operation_id=?", id).Scan(&eventCount)
	if err != nil || eventCount != 1 {
		return zero, errors.Join(ErrConflict, err)
	}
	err = q.QueryRowContext(ctx, "SELECT canonical FROM data_native_backup_resolution_events WHERE operation_id=? AND stage='claimed'", id).Scan(&claimRaw)
	reviewRaw, _ := json.Marshal(review)
	if err != nil || !bytes.Equal(claimRaw, reviewRaw) {
		return zero, errors.Join(ErrConflict, err)
	}
	operationRaw, err := json.Marshal(operation)
	if err != nil {
		return zero, err
	}
	return previousResolutionClaim{OperationSHA256: nativePointHash(operationRaw), OutcomeSHA256: nativePointHash(outcomeRaw), ClaimSHA256: nativePointHash(claimRaw)}, nil
}
