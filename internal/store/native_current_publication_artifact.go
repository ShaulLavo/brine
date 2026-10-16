package store

import (
	"context"
	"database/sql"
	"errors"

	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/datapublish"
)

// ReadNativeCurrentPublicationArtifact reads the exact current reviewed Wasm.
// Native program-bytes is a bootstrap cache and can retain only the first module.
// The caller still authenticates the live module and extracts its descriptor.
func (s *Store) ReadNativeCurrentPublicationArtifact(ctx context.Context, binding data.ReplicaBindingID) (datapublish.Artifact, error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return datapublish.Artifact{}, err
	}
	defer func() { _ = tx.Rollback() }()
	permit, err := readEnginePermit(ctx, tx, binding)
	if err != nil || permit.Runtime.State != data.EngineInitialized {
		return datapublish.Artifact{}, ErrConflict
	}
	epoch, err := nativePublicationArtifactEpoch(ctx, tx, binding, permit.Runtime.EpochID)
	if err != nil {
		return datapublish.Artifact{}, err
	}
	var planID string
	err = tx.QueryRowContext(ctx, "SELECT plan_id FROM data_module_publication_claims WHERE binding_id=? AND new_epoch_id=?", binding, epoch).Scan(&planID)
	if errors.Is(err, sql.ErrNoRows) {
		return datapublish.Artifact{}, ErrNotFound
	}
	if err != nil {
		return datapublish.Artifact{}, err
	}
	raw, index, err := readPublicationReviewTx(ctx, tx, planID)
	if err != nil || index.binding != binding {
		return datapublish.Artifact{}, ErrConflict
	}
	review, err := datapublish.DecodePrivateReview(raw)
	if err != nil {
		return datapublish.Artifact{}, &IntegrityError{}
	}
	artifact := review.Artifact()
	if string(artifact.WasmSHA256()) != string(permit.Runtime.Module.Wasm) || string(artifact.ProgramWireHash()) != string(permit.Runtime.Module.Program) || review.Plan().Candidate().Schema().SHA256() != permit.Runtime.Module.Schema.SHA256 {
		return datapublish.Artifact{}, ErrConflict
	}
	return artifact, tx.Commit()
}

func nativePublicationArtifactEpoch(ctx context.Context, q dataQuerier, binding data.ReplicaBindingID, epoch data.ReplicaEpochID) (data.ReplicaEpochID, error) {
	seen := map[data.ReplicaEpochID]bool{}
	for {
		if seen[epoch] {
			return "", &IntegrityError{}
		}
		seen[epoch] = true
		reserved, _, err := readNativeEpoch(ctx, q, binding, epoch)
		if errors.Is(err, ErrNotFound) {
			return epoch, nil
		}
		if err != nil {
			return "", err
		}
		transition, cursor, err := readEngineTransition(ctx, q, reserved.Record().TransitionID)
		if err != nil || !reserved.Matches(transition) {
			return "", errors.Join(&IntegrityError{}, err)
		}
		if cursor.Stage != data.EngineTransitionCompleted {
			return "", ErrConflict
		}
		restored, ok := transition.Restoration()
		if !ok {
			return epoch, nil
		}
		epoch = restored.SelectedPoint.EpochID
	}
}
