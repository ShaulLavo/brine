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
	var planID string
	err = tx.QueryRowContext(ctx, "SELECT plan_id FROM data_module_publication_claims WHERE binding_id=? AND new_epoch_id=?", binding, permit.Runtime.EpochID).Scan(&planID)
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
