package store

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"slices"
	"time"

	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/datapublish"
	"github.com/ShaulLavo/brine/internal/nativecapture"
	"github.com/ShaulLavo/brine/internal/nativeevidence"
	"github.com/ShaulLavo/brine/internal/spacetimedb"
)

// PublicationVerificationObservation is constructed by the fixed passive
// reader over the retained private root. It is never a request body.
type PublicationVerificationObservation struct {
	Checkpoint       nativecapture.Checkpoint
	Evidence         nativeevidence.Evidence
	ReaderNonce      string
	ReaderViewSHA256 string
	ReaderImageID    string
	ReaderDisposedAt time.Time
}

func (PublicationVerificationObservation) MarshalJSON() ([]byte, error) { return nil, ErrInvalid }

type publicationVerificationRecord struct {
	Format            string                         `json:"format"`
	Scope             datapublish.ProofScope         `json:"scope"`
	Checkpoint        json.RawMessage                `json:"checkpoint"`
	Evidence          json.RawMessage                `json:"native_evidence"`
	DataProof         json.RawMessage                `json:"data_proof"`
	Requirements      []datapublish.RequirementProof `json:"requirements"`
	ApprovalSHA256    string                         `json:"approval_sha256"`
	PublicationSHA256 string                         `json:"publication_sha256"`
	ReaderNonce       string                         `json:"reader_nonce"`
	ReaderViewSHA256  string                         `json:"reader_view_sha256"`
	ReaderImageID     string                         `json:"reader_image_id"`
	ReaderDisposedAt  time.Time                      `json:"reader_disposed_at"`
	VerifiedAt        time.Time                      `json:"verified_at"`
}

func (s *Store) RecordPublicationVerified(ctx context.Context, attempt *PublicationAttempt, observed PublicationVerificationObservation) (datapublish.Cursor, error) {
	if !attempt.active(s) || attempt.restored.unchanged(ctx) != nil {
		return datapublish.Cursor{}, ErrConflict
	}
	tx, cancel, err := s.beginWrite(ctx)
	defer cancel()
	if err != nil {
		return datapublish.Cursor{}, err
	}
	defer func() { _ = tx.Rollback() }()
	current, err := s.readPublicationAttemptTx(ctx, tx, attempt)
	if err != nil || current.cursor.State() != datapublish.IntentRecorded || current.cursor.IntendedStep() != datapublish.VerifyStage {
		return datapublish.Cursor{}, errors.Join(ErrConflict, err)
	}
	approval, err := readPublicationReviewApprovalTx(ctx, tx, current.review, current.index)
	if err != nil {
		return current.cursor, err
	}
	published, publicationSHA, err := readPublicationPublishedTx(ctx, tx, current.cursor)
	if err != nil || checkPublicationVerificationObservation(ctx, attempt, current, published, observed) != nil {
		return current.cursor, errors.Join(ErrConflict, err)
	}
	if _, err = checkPublicationStageDisposedTx(ctx, tx, attempt, current.cursor, datapublish.PublishStage, published.Process); err != nil {
		return current.cursor, err
	}
	bundle, err := readNativePreparedBundleTx(ctx, tx, current.point.view.PointID)
	if err != nil {
		return current.cursor, err
	}
	approvalRaw, _ := json.Marshal(approval)
	dataProof, requirements, err := derivePublicationRequirementProofs(current.review.Plan(), bundle.sourceEvidence, observed.Evidence, publicationStorageSHA256(approvalRaw))
	if err != nil {
		return current.cursor, err
	}
	record := publicationVerificationRecord{Format: "brine-publication-target-verification-v1", Scope: current.cursor.Scope(), Checkpoint: observed.Checkpoint.Bytes(), Evidence: observed.Evidence.Bytes(), DataProof: dataProof, Requirements: requirements,
		ApprovalSHA256: publicationStorageSHA256(approvalRaw), PublicationSHA256: publicationSHA, ReaderNonce: observed.ReaderNonce, ReaderViewSHA256: observed.ReaderViewSHA256, ReaderImageID: observed.ReaderImageID, ReaderDisposedAt: observed.ReaderDisposedAt, VerifiedAt: time.Now().UTC()}
	raw, err := json.Marshal(record)
	if err != nil || len(raw) > 4<<20 {
		return current.cursor, ErrInvalid
	}
	observation := publicationTargetObservation(current.cursor, datapublish.VerifyStage, publicationStorageSHA256(raw))
	observation.Requirements = requirements
	next, err := current.cursor.Observe(observation)
	if err != nil {
		return current.cursor, ErrConflict
	}
	if err = insertPublicationProofTx(ctx, tx, attempt.planID, "verification", publicationVerificationProof, raw); err != nil {
		return current.cursor, err
	}
	if err = insertPublicationProofTx(ctx, tx, attempt.planID, "checkpoint", publicationCheckpointProof, observed.Checkpoint.Bytes()); err != nil {
		return current.cursor, err
	}
	if err = appendPublicationCursorTx(ctx, tx, current.review.Plan(), current.cursor, next); err != nil || attempt.restored.unchanged(ctx) != nil || checkPublicationCheckpointMembers(ctx, attempt.restored.root, observed.Checkpoint) != nil {
		return current.cursor, errors.Join(ErrConflict, err)
	}
	return next, tx.Commit()
}

func checkPublicationVerificationObservation(ctx context.Context, attempt *PublicationAttempt, current publicationAttemptState, published PublicationStageObservation, observed PublicationVerificationObservation) error {
	plan, scope := current.review.Plan(), current.cursor.Scope()
	c := observed.Checkpoint.Record()
	if observed.Checkpoint.SHA256() == "" || c.Scope.BindingID != scope.BindingID || c.Scope.EpochID != scope.NewEpochID || c.DatabaseIdentity != current.cursor.NativeDatabase() || c.PublisherIdentity != current.cursor.NativePublisher() || c.RuntimeModuleHash != scope.CandidateProgramWireHash || c.ArtifactSHA256 != scope.CandidateWasmSHA256 || c.SchemaVersion != plan.Candidate().Schema().Version() || c.SchemaSHA256 != scope.CandidateSchemaSHA256 || c.ConfigurationSHA256 != current.point.view.ConfigSHA256 || !nativeSigningRecoveryMatches(c.SigningRecovery, plan.Facts().SecretVersions.SigningPrivate) {
		return ErrConflict
	}
	names := current.point.view.Names
	if plan.Source().Kind() == datapublish.CreateSource {
		names = []string{plan.CreationName()}
	}
	if !slices.Equal(c.Names, names) || !data.ValidID(observed.ReaderNonce) || !data.ValidCatalogHash(observed.ReaderViewSHA256) || !data.ValidCatalogHash(observed.ReaderImageID) || observed.ReaderDisposedAt.IsZero() || observed.ReaderDisposedAt.Location() != time.UTC || observed.ReaderDisposedAt.Before(published.Process.FinishedAt) || observed.ReaderDisposedAt.After(time.Now().UTC()) {
		return ErrConflict
	}
	evidence, err := nativeevidence.Decode(observed.Evidence.Bytes(), observed.Evidence.RequestedLimits())
	p := evidence.Program()
	if err != nil || evidence.SHA256() != observed.Evidence.SHA256() || evidence.DatabaseIdentity() != c.DatabaseIdentity || evidence.OwnerIdentity() != c.PublisherIdentity || p.Kind != "wasm" || p.Wire != c.RuntimeModuleHash || p.SHA256 != c.ArtifactSHA256 || evidence.PersistedConnectedClients() != 0 {
		return ErrConflict
	}
	if snapshot, present := evidence.Snapshot(); present && snapshot.Replica != c.Scope.ReplicaID {
		return ErrConflict
	}
	return checkPublicationCheckpointMembers(ctx, attempt.restored.root, observed.Checkpoint)
}

func derivePublicationRequirementProofs(plan datapublish.Plan, source, target nativeevidence.Evidence, approvalSHA string) ([]byte, []datapublish.RequirementProof, error) {
	if !data.ValidCatalogHash(approvalSHA) {
		return nil, nil, ErrConflict
	}
	pairs, err := spacetimedb.RecoveryPairsFromPassive(target, plan.Candidate().Schema())
	if err != nil {
		return nil, nil, err
	}
	var raw []byte
	var dataSHA string
	var qualified []spacetimedb.SchemaRequirement
	if plan.Source().Kind() == datapublish.CreateSource {
		proof, err := spacetimedb.CheckRecoveryPairsInitial(pairs)
		if err != nil {
			return nil, nil, err
		}
		raw, dataSHA, qualified = proof.Bytes(), proof.SHA256(), proof.Requirements()
	} else {
		original, err := spacetimedb.RecoveryPairsFromPassive(source, plan.Source().Schema())
		if err != nil {
			return nil, nil, err
		}
		proof, err := spacetimedb.CheckRecoveryPairPreservation(original, pairs)
		if err != nil {
			return nil, nil, err
		}
		raw, dataSHA, qualified = proof.Bytes(), proof.SHA256(), proof.Requirements()
	}
	proofs := make([]datapublish.RequirementProof, 0, len(plan.Requirements()))
	for _, requirement := range plan.Requirements() {
		hash := dataSHA
		if requirement == spacetimedb.ReviewGuestCode {
			hash = approvalSHA
		} else if !slices.Contains(qualified, requirement) {
			return nil, nil, ErrConflict
		}
		proofs = append(proofs, datapublish.RequirementProof{Requirement: requirement, SHA256: hash})
	}
	return raw, proofs, nil
}

func publicationTargetObservation(cursor datapublish.Cursor, step datapublish.Step, hash string) datapublish.Observation {
	scope := cursor.Scope()
	return datapublish.Observation{Scope: scope, Step: step, Outcome: datapublish.ObservedCompleted, ProofSHA256: hash, NativeDatabase: cursor.NativeDatabase(), NativePublisher: cursor.NativePublisher(), ProgramWireHash: scope.CandidateProgramWireHash, SchemaSHA256: scope.CandidateSchemaSHA256, Requirements: []datapublish.RequirementProof{}}
}

func insertPublicationProofTx(ctx context.Context, tx *sql.Tx, plan, id string, kind publicationProofKind, raw []byte) error {
	if len(raw) == 0 || len(raw) > 4<<20 {
		return ErrInvalid
	}
	_, err := tx.ExecContext(ctx, "INSERT INTO data_module_publication_proofs VALUES(?,?,?,?,?)", plan, id, kind, publicationStorageSHA256(raw), raw)
	return err
}

func readPublicationProofTx(ctx context.Context, tx *sql.Tx, plan, id, kind string) ([]byte, string, error) {
	var raw []byte
	var hash string
	err := tx.QueryRowContext(ctx, "SELECT evidence_sha256,canonical FROM data_module_publication_proofs WHERE plan_id=? AND id=? AND kind=?", plan, id, kind).Scan(&hash, &raw)
	if err != nil {
		return nil, "", err
	}
	if len(raw) == 0 || len(raw) > 4<<20 || publicationStorageSHA256(raw) != hash {
		return nil, "", &IntegrityError{}
	}
	return raw, hash, nil
}

func readPublicationPublishedTx(ctx context.Context, tx *sql.Tx, cursor datapublish.Cursor) (PublicationStageObservation, string, error) {
	var published PublicationStageObservation
	raw, hash, err := readPublicationProofTx(ctx, tx, cursor.Scope().PlanID, "publication", "publication_receipt")
	if err != nil || json.Unmarshal(raw, &published) != nil {
		return published, "", errors.Join(&IntegrityError{}, err)
	}
	canonical, _ := json.Marshal(published)
	if !bytes.Equal(raw, canonical) || published.NativeDatabase != cursor.NativeDatabase() || published.NativePublisher != cursor.NativePublisher() || published.ProgramWireHash != cursor.Scope().CandidateProgramWireHash || published.SchemaSHA256 != cursor.Scope().CandidateSchemaSHA256 || !published.Process.Disposed {
		return published, "", &IntegrityError{}
	}
	return published, hash, nil
}
