package store

import (
	"context"
	"encoding/json"

	"github.com/ShaulLavo/brine/internal/cdn"
	"github.com/ShaulLavo/brine/internal/ops"
)

func nextCDN(status cdn.Status) cdn.Work {
	if status.State == string(ops.Succeeded) {
		return cdn.Finished{Operation: status.Operation, Revision: status.Plan.Context.Attachment.Revision, Succeeded: true}
	}
	if status.State == string(ops.Failed) {
		return cdn.Finished{Operation: status.Operation, Succeeded: false}
	}
	for _, effect := range status.Effects {
		if effect.Stage == cdn.Rejected {
			return cdn.Waiting{Code: "effect_refused_new_plan_required"}
		}
		if effect.Stage != cdn.Confirmed {
			return effect.Next()
		}
	}
	index := uint32(len(status.Effects))
	if int(index) == len(status.Plan.Steps) {
		return cdn.NeedObservation{Request: cdn.NeedQualification{Qualification: cdn.Qualification{Attachment: status.Plan.Context.Attachment, Sample: status.Plan.Sample}, Receipts: finalCDNReceipts(status)}}
	}
	step := status.Plan.Steps[index]
	return cdn.NeedObservation{Request: cdn.PrepareStep{Operation: status.Operation, StepIndex: index, Attachment: status.Plan.Context.Attachment, Step: step, Prior: priorCDNReceipt(status, index)}}
}

func priorCDNReceipt(status cdn.Status, index uint32) cdn.OwnedReceipt {
	step := status.Plan.Steps[index]
	if source, ok := step.Precondition.(cdn.PriorOwnedReceipt); ok {
		return effectCDNReceipt(status.Effects[source.StepIndex])
	}
	if step.Kind != cdn.SetTraffic {
		return cdn.OwnedReceipt{}
	}
	for n := int(index) - 1; n >= 0; n-- {
		if status.Effects[n].Permit.Action.Profile == status.Plan.Context.Attachment.Profile {
			return effectCDNReceipt(status.Effects[n])
		}
	}
	for _, observed := range status.Plan.Context.Initial {
		if observed.Profile == status.Plan.Context.Attachment.Profile {
			return cdn.OwnedReceipt{Effect: cdn.EffectID(cdn.Hash([]byte("initial:" + string(status.Plan.ID)))), Profile: observed.Profile, Attachment: status.Plan.Context.Attachment, Resources: observed.Resources}
		}
	}
	return cdn.OwnedReceipt{}
}

func effectCDNReceipt(effect cdn.Effect) cdn.OwnedReceipt {
	return cdn.OwnedReceipt{Effect: effect.Permit.Effect, Profile: effect.Permit.Action.Profile, Attachment: effect.Permit.Action.Attachment, Resources: effect.Resources}
}

func (s *Store) NextCDN(ctx context.Context, operation string) (cdn.Work, error) {
	status, err := s.CDNStatus(ctx, operation)
	if err != nil {
		return nil, err
	}
	return nextCDN(status), nil
}

func (s *Store) ClaimCDN(ctx context.Context, operation string, index uint32, sealed cdn.SealedAction) (cdn.Work, error) {
	tx, cancel, err := s.beginWrite(ctx)
	defer cancel()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	status, op, err := loadCDNStatus(ctx, tx, operation)
	if err != nil {
		return nil, err
	}
	// The durable winner owns the only permit. A lost response is inspect-only.
	if int(index) < len(status.Effects) {
		return nextCDN(status), nil
	}
	if status.State != string(ops.Preparing) || index != uint32(len(status.Effects)) || int(index) >= len(status.Plan.Steps) {
		return nil, ErrConflict
	}
	work, ok := nextCDN(status).(cdn.NeedObservation)
	if !ok {
		return nil, ErrConflict
	}
	request, ok := work.Request.(cdn.PrepareStep)
	if !ok {
		return nil, ErrConflict
	}
	if err = cdn.ValidateSealed(request.Attachment, request.Step, sealed, request.Prior); err != nil {
		return nil, err
	}
	if !freshCDNObservation(sealed.Current, op.CreatedAt) || !freshCDNPrerequisites(sealed, op) {
		return nil, ErrConflict
	}
	if request.Step.Kind == cdn.SetTraffic && (!sealed.OriginHealthy || !digestPattern.MatchString(string(sealed.OriginEvidence))) {
		return nil, ErrConflict
	}
	permit, err := status.Plan.NewPermit(status.Operation, index, sealed.Action)
	if err != nil {
		return nil, err
	}
	claim, err := newID()
	if err != nil {
		return nil, err
	}
	effect, need, err := (cdn.Effect{Permit: permit, Stage: cdn.Planned}).Claim(cdn.ClaimID(claim))
	if err != nil {
		return nil, err
	}
	permitRaw, _ := json.Marshal(effect.Permit)
	raw, _ := json.Marshal(effect)
	_, err = tx.ExecContext(ctx, "INSERT INTO cdn_effects VALUES(?,?,?,?)", operation, index, permitRaw, raw)
	if err != nil {
		return nil, err
	}
	if err = cdnEvidence(ctx, tx, operation, "seal", sealed); err != nil {
		return nil, err
	}
	if err = cdnEvent(ctx, tx, operation, status.Plan.ID, effect.Permit.Effect, string(effect.Stage), ""); err != nil {
		return nil, err
	}
	return need, tx.Commit()
}

func freshCDNPrerequisites(sealed cdn.SealedAction, op ops.Operation) bool {
	for _, observed := range sealed.Prerequisites {
		if !freshCDNObservation(observed, op.CreatedAt) {
			return false
		}
	}
	return true
}

func (s *Store) RecordCDNResult(ctx context.Context, operation string, result cdn.EffectResult) (cdn.Work, error) {
	return s.updateCDNEffect(ctx, operation, "result", result, func(effect cdn.Effect) (cdn.Effect, cdn.Work, error) {
		next, err := effect.Record(result)
		return next, nil, err
	})
}

func (s *Store) RecordCDNInspection(ctx context.Context, operation string, inspection cdn.Inspection) (cdn.Work, error) {
	return s.updateCDNEffect(ctx, operation, "inspection", inspection, func(effect cdn.Effect) (cdn.Effect, cdn.Work, error) { return effect.Inspect(inspection) })
}

func (s *Store) updateCDNEffect(ctx context.Context, operation, kind string, evidence any, update func(cdn.Effect) (cdn.Effect, cdn.Work, error)) (cdn.Work, error) {
	tx, cancel, err := s.beginWrite(ctx)
	defer cancel()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	status, _, err := loadCDNStatus(ctx, tx, operation)
	if err != nil {
		return nil, err
	}
	if status.State != string(ops.Preparing) || len(status.Effects) == 0 {
		return nil, ErrConflict
	}
	index := len(status.Effects) - 1
	effect, observedWork, err := update(status.Effects[index])
	if err != nil {
		return nil, err
	}
	raw, _ := json.Marshal(effect)
	_, err = tx.ExecContext(ctx, "UPDATE cdn_effects SET record=? WHERE operation_id=? AND step_index=?", raw, operation, index)
	if err != nil {
		return nil, err
	}
	if err = cdnEvidence(ctx, tx, operation, kind, evidence); err != nil {
		return nil, err
	}
	if err = cdnEvent(ctx, tx, operation, status.Plan.ID, effect.Permit.Effect, string(effect.Stage), effect.Completion); err != nil {
		return nil, err
	}
	status.Effects[index] = effect
	if waiting, ok := observedWork.(cdn.Waiting); ok {
		return waiting, tx.Commit()
	}
	return nextCDN(status), tx.Commit()
}
