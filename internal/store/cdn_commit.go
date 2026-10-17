package store

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"slices"
	"strings"

	"github.com/ShaulLavo/brine/internal/cdn"
	"github.com/ShaulLavo/brine/internal/ops"
)

type cdnResourceKey struct {
	Profile cdn.ProfileScope
	Kind    cdn.ResourceKind
	Name    string
}

func (s *Store) CDNReceipts(ctx context.Context, operation string) ([]cdn.OwnedReceipt, error) {
	status, err := s.CDNStatus(ctx, operation)
	if err != nil {
		return nil, err
	}
	return finalCDNReceipts(status), nil
}

func finalCDNReceipts(status cdn.Status) []cdn.OwnedReceipt {
	resources := map[cdnResourceKey]cdn.OwnedReceipt{}
	for _, receipt := range status.Plan.Context.Retained {
		collectCDNResources(resources, receipt, status.Plan.Context.Attachment.Owner)
	}
	for _, observed := range status.Plan.Context.Initial {
		receipt := cdn.OwnedReceipt{Effect: cdn.EffectID(cdn.Hash([]byte("initial:" + string(status.Plan.ID)))), Profile: observed.Profile, Attachment: status.Plan.Context.Attachment, Resources: observed.Resources}
		collectCDNResources(resources, receipt, status.Plan.Context.Attachment.Owner)
	}
	for _, effect := range status.Effects {
		collectCDNResources(resources, effectCDNReceipt(effect), status.Plan.Context.Attachment.Owner)
	}
	result := make([]cdn.OwnedReceipt, 0, len(resources))
	for _, receipt := range resources {
		result = append(result, receipt)
	}
	slices.SortFunc(result, func(a, b cdn.OwnedReceipt) int {
		return strings.Compare(string(a.Profile.Reference)+"\n"+string(a.Resources[0].Kind)+"\n"+a.Resources[0].ExactName, string(b.Profile.Reference)+"\n"+string(b.Resources[0].Kind)+"\n"+b.Resources[0].ExactName)
	})
	return result
}

func collectCDNResources(resources map[cdnResourceKey]cdn.OwnedReceipt, receipt cdn.OwnedReceipt, owner cdn.Owner) {
	for _, resource := range receipt.Resources {
		if resource.Owner != owner {
			continue
		}
		key := cdnResourceKey{receipt.Profile, resource.Kind, resource.ExactName}
		resources[key] = cdn.OwnedReceipt{Effect: receipt.Effect, Profile: receipt.Profile, Attachment: receipt.Attachment, Resources: []cdn.OwnedResource{resource}}
	}
}

func (s *Store) CommitCDN(ctx context.Context, operation string, proof cdn.PublicProof, readback []cdn.Observation) (cdn.Work, error) {
	tx, cancel, err := s.beginWrite(ctx)
	defer cancel()
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	status, op, err := loadCDNStatus(ctx, tx, operation)
	if err != nil {
		return nil, err
	}
	if status.State == string(ops.Succeeded) {
		return nextCDN(status), nil
	}
	if status.State != string(ops.Preparing) || len(status.Effects) != len(status.Plan.Steps) {
		return nil, ErrConflict
	}
	for _, effect := range status.Effects {
		if effect.Stage != cdn.Confirmed {
			return nil, ErrConflict
		}
	}
	a := status.Plan.Context.Attachment
	if proof.Validate(a, status.Plan.Sample) != nil || proof.ObservedAt.Before(op.CreatedAt) {
		return nil, ErrInvalid
	}
	receipts := finalCDNReceipts(status)
	for _, receipt := range receipts {
		if !readbackCDNReceipt(receipt, readback, op) {
			return nil, ErrConflict
		}
	}
	for _, profile := range []cdn.ProfileScope{a.Profile, a.Binding.Traffic} {
		if !readbackCDNProfile(profile, readback, op) {
			return nil, ErrConflict
		}
	}
	revision := cdn.Revision{Attachment: a, Plan: status.Plan.ID, Operation: status.Operation, Proof: proof, Receipts: receipts, Readback: readback}
	raw, _ := json.Marshal(revision)
	head, headErr := readCDNRevision(ctx, tx, a.Owner.App, "")
	if headErr != nil && !errors.Is(headErr, ErrNotFound) {
		return nil, headErr
	}
	if headErr == nil && head.Attachment.Revision == a.Revision {
		if len(status.Plan.Steps) != 0 || !reflect.DeepEqual(head.Attachment, a) {
			return nil, ErrConflict
		}
	} else {
		_, err = tx.ExecContext(ctx, "INSERT INTO cdn_revisions VALUES(?,?,?,?)", a.Revision, a.Owner.App, a.Owner.Attachment, raw)
		if err != nil {
			return nil, err
		}
		_, err = tx.ExecContext(ctx, "INSERT INTO cdn_heads VALUES(?,?) ON CONFLICT(app) DO UPDATE SET revision_id=excluded.revision_id", a.Owner.App, a.Revision)
		if err != nil {
			return nil, err
		}
	}
	proofRaw, _ := json.Marshal(proof)
	readbackRaw, _ := json.Marshal(readback)
	_, err = tx.ExecContext(ctx, "UPDATE cdn_sessions SET proof=?,readback=? WHERE operation_id=?", proofRaw, readbackRaw, operation)
	if err != nil {
		return nil, err
	}
	if err = cdnEvidence(ctx, tx, operation, "qualification", revision); err != nil {
		return nil, err
	}
	if err = cdnEvent(ctx, tx, operation, status.Plan.ID, "", "qualified", proof.Evidence); err != nil {
		return nil, err
	}
	if err = cdnState(ctx, tx, operation, ops.Succeeded); err != nil {
		return nil, err
	}
	_, err = tx.ExecContext(ctx, "DELETE FROM cdn_reservations WHERE operation_id=?", operation)
	if err != nil {
		return nil, err
	}
	return cdn.Finished{Operation: status.Operation, Revision: a.Revision, Succeeded: true}, tx.Commit()
}

func readbackCDNProfile(profile cdn.ProfileScope, readback []cdn.Observation, op ops.Operation) bool {
	for _, observed := range readback {
		if observed.Profile == profile && freshCDNObservation(observed, op.CreatedAt) {
			return true
		}
	}
	return false
}

func readbackCDNReceipt(receipt cdn.OwnedReceipt, readback []cdn.Observation, op ops.Operation) bool {
	for _, observed := range readback {
		if observed.Profile != receipt.Profile || !freshCDNObservation(observed, op.CreatedAt) {
			continue
		}
		if hasCDNResources(observed.Resources, receipt.Resources) {
			return true
		}
	}
	return false
}

func hasCDNResources(actual, expected []cdn.OwnedResource) bool {
	for _, resource := range expected {
		if !slices.Contains(actual, resource) {
			return false
		}
	}
	return true
}
