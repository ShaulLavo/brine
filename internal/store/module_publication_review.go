package store

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"slices"
	"strings"

	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/datapublish"
)

type publicationReviewBinding struct {
	pointID        string
	binding        data.ReplicaBindingID
	requester      string
	generation     uint64
	generationID   string
	pointSHA       string
	runtimeVersion uint64
	runtimeSHA     string
}

func publicationStorageSHA256(raw []byte) string {
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func publicationPlanMatchesPoint(plan datapublish.Plan, point NativePreparedPointView) bool {
	facts, source := plan.Facts(), plan.Source()
	if facts.BindingID != string(point.BindingID) || facts.DatabaseID != string(point.DatabaseID) || facts.IncarnationID != string(point.IncarnationID) || facts.EpochID != string(point.EpochID) || facts.Generation != point.Generation || source.PointID() != point.PointID || source.FenceID() != string(point.FenceID) || source.PreparationOpID() != point.PreparationOperationID || source.SealSHA256() != point.SourceSealSHA256 {
		return false
	}
	if empty, ok := source.Empty(); ok {
		return point.SourceKind == "unpublished" && empty.AllocationID == point.AllocationID && empty.ConfigSHA256 == point.ConfigSHA256 && empty.SigningRecoverySHA256 == point.SigningRecoverySHA256 && empty.RestoreReceiptSHA256 == point.RestoreReceiptSHA256 && empty.BundleManifestSHA256 == point.BundleManifestSHA256
	}
	captured, ok := source.Captured()
	return ok && point.SourceKind == "initialized" && captured.NativeDatabase == point.NativeDatabase && captured.NativePublisher == point.NativePublisher && captured.ProgramWireHash == point.ProgramWireHash && captured.WasmSHA256 == point.ArtifactWasmSHA256 && source.Schema().SHA256() == point.SchemaSHA256 && captured.StateWitnessSHA256 == point.SourceWitnessSHA256 && captured.StopProofSHA256 == point.StopProofSHA256 && captured.CheckpointSHA256 == point.CheckpointSHA256 && captured.RestoreReceiptSHA256 == point.RestoreReceiptSHA256 && captured.BundleManifestSHA256 == point.BundleManifestSHA256 && slices.Equal(captured.Names, point.Names)
}

func checkPublicationPolicy(ctx context.Context, tx *sql.Tx, facts datapublish.BindingFacts) error {
	var policyHash string
	err := tx.QueryRowContext(ctx, "SELECT policy_hash FROM data_admissions WHERE database_id=? ORDER BY version DESC LIMIT 1", facts.DatabaseID).Scan(&policyHash)
	if err != nil || strings.TrimPrefix(policyHash, "sha256:") != facts.PolicySHA256 {
		return errors.Join(ErrConflict, err)
	}
	return nil
}

func insertPublicationReview(ctx context.Context, tx *sql.Tx, planID string, index publicationReviewBinding, raw []byte) error {
	_, err := tx.ExecContext(ctx, "INSERT INTO data_module_publication_reviews VALUES(?,?,?,?,?,?,?,?,?,?,?) ON CONFLICT(plan_id) DO NOTHING", planID, index.pointID, index.binding, index.requester, index.generation, index.generationID, index.pointSHA, index.runtimeVersion, index.runtimeSHA, publicationStorageSHA256(raw), raw)
	if err != nil {
		return err
	}
	retained, oldIndex, err := readPublicationReviewTx(ctx, tx, planID)
	if err != nil || oldIndex != index || !bytes.Equal(retained, raw) {
		return errors.Join(ErrConflict, err)
	}
	return nil
}

func readPublicationReviewTx(ctx context.Context, tx *sql.Tx, planID string) ([]byte, publicationReviewBinding, error) {
	var index publicationReviewBinding
	var raw []byte
	var retainedSHA string
	err := tx.QueryRowContext(ctx, "SELECT point_id,binding_id,requester,generation,generation_id,point_sha256,runtime_version,runtime_sha256,storage_sha256,canonical FROM data_module_publication_reviews WHERE plan_id=?", planID).Scan(&index.pointID, &index.binding, &index.requester, &index.generation, &index.generationID, &index.pointSHA, &index.runtimeVersion, &index.runtimeSHA, &retainedSHA, &raw)
	if err != nil {
		return nil, index, err
	}
	if len(raw) == 0 || len(raw) > datapublish.PrivateReviewLimit || publicationStorageSHA256(raw) != retainedSHA {
		return nil, index, &IntegrityError{}
	}
	review, err := datapublish.DecodePrivateReview(raw)
	if err != nil || review.Plan().ID() != planID || review.Plan().Source().PointID() != index.pointID || review.Plan().Facts().BindingID != string(index.binding) || review.Plan().Requester().String() != index.requester || review.Plan().Facts().Generation != index.generation {
		return nil, index, &IntegrityError{}
	}
	return bytes.Clone(raw), index, nil
}

func (s *Store) ReadPublicationReview(ctx context.Context, planID string) (datapublish.PrivateReview, error) {
	if !digestPattern.MatchString(planID) {
		return datapublish.PrivateReview{}, ErrInvalid
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return datapublish.PrivateReview{}, err
	}
	defer func() { _ = tx.Rollback() }()
	raw, _, err := readPublicationReviewTx(ctx, tx, planID)
	if err != nil {
		return datapublish.PrivateReview{}, err
	}
	review, err := datapublish.DecodePrivateReview(raw)
	if err != nil {
		return datapublish.PrivateReview{}, &IntegrityError{}
	}
	return review, tx.Commit()
}
