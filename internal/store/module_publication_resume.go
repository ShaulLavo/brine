package store

import (
	"context"
	"database/sql"
	"errors"

	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/ops"
)

func (s *Store) ResumePublicationAttempt(ctx context.Context, preparation *EnginePreparation, planID, originalID, resolverID string) (*PublicationAttempt, error) {
	if s.readOnly || preparation == nil || preparation.StateRoot() != s.dir || !digestPattern.MatchString(planID) || originalID == "" || resolverID == "" || originalID == resolverID {
		return nil, ErrConflict
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	claim, err := readPublicationClaimTx(ctx, tx, planID)
	if err != nil || claim.ApplyOpID != originalID || !preparation.Active(claim.BindingID) {
		return nil, errors.Join(ErrConflict, err)
	}
	var outcome []byte
	if err = tx.QueryRowContext(ctx, "SELECT canonical FROM operation_outcomes WHERE operation_id=?", originalID).Scan(&outcome); err != nil {
		return nil, err
	}
	cap := &PublicationAttempt{store: s, preparation: preparation, planID: planID, operationID: originalID, binding: claim.BindingID, resolverID: resolverID, originalOutcomeSHA: publicationStorageSHA256(outcome)}
	if err = s.checkPublicationResolverTx(ctx, tx, cap); err != nil {
		return nil, err
	}
	if _, err = s.readPublicationAttemptTx(ctx, tx, cap); err != nil {
		return nil, err
	}
	return cap, tx.Commit()
}

func (s *Store) checkPublicationResolverTx(ctx context.Context, tx *sql.Tx, attempt *PublicationAttempt) error {
	if !attempt.active(s) || attempt.resolverID == "" || !data.ValidCatalogHash(attempt.originalOutcomeSHA) {
		return ErrConflict
	}
	original, err := scanOperation(tx.QueryRowContext(ctx, "SELECT id,COALESCE(plan_id,''),requester,idempotency_key,state,created_at,updated_at,kind,app,secret_ref,recovery_of FROM operations WHERE id=?", attempt.operationID))
	if err != nil || original.Kind != ops.ModulePublishApply || original.State != ops.RecoveryRequired || original.SecretRef != attempt.planID {
		return errors.Join(ErrConflict, err)
	}
	resolver, err := scanOperation(tx.QueryRowContext(ctx, "SELECT id,COALESCE(plan_id,''),requester,idempotency_key,state,created_at,updated_at,kind,app,secret_ref,recovery_of FROM operations WHERE id=?", attempt.resolverID))
	if err != nil || resolver.Kind != ops.ModulePublishResolve || resolver.State != ops.Preflight || resolver.RecoveryOf != original.ID || resolver.SecretRef != attempt.planID || resolver.App != original.App || resolver.Requester != original.Requester {
		return errors.Join(ErrConflict, err)
	}
	var raw []byte
	if err = tx.QueryRowContext(ctx, "SELECT canonical FROM operation_outcomes WHERE operation_id=?", original.ID).Scan(&raw); err != nil {
		return err
	}
	if _, err = ops.DecodeTaskOutcome(original, raw); err != nil || publicationStorageSHA256(raw) != attempt.originalOutcomeSHA {
		return &IntegrityError{}
	}
	return nil
}
