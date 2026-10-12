package store

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"time"

	"github.com/ShaulLavo/brine/internal/datapublish"
	"github.com/ShaulLavo/brine/internal/nativeevidence"
	"github.com/ShaulLavo/brine/internal/restic"
)

func (s *Store) RecordPublicationRestore(ctx context.Context, attempt *PublicationAttempt, restored restic.AuthenticatedRestore, parent *os.Root, evidence nativeevidence.Evidence) (datapublish.Cursor, error) {
	tx, cancel, err := s.beginWrite(ctx)
	defer cancel()
	if err != nil {
		return datapublish.Cursor{}, err
	}
	defer func() { _ = tx.Rollback() }()
	current, err := s.readPublicationAttemptTx(ctx, tx, attempt)
	if err != nil || current.cursor.State() != datapublish.IntentRecorded || current.cursor.IntendedStep() != datapublish.RestoreStage {
		return datapublish.Cursor{}, errors.Join(ErrConflict, err)
	}
	bundle, err := readNativePreparedBundleTx(ctx, tx, current.point.view.PointID)
	if err != nil || !restored.Matches(ctx, parent, "download", bundle.transport, bundle.manifest) {
		return current.cursor, errors.Join(ErrConflict, err)
	}
	if current.point.view.SourceKind == "initialized" && !evidence.SamePersistedState(bundle.sourceEvidence) || current.point.view.SourceKind == "unpublished" && evidence.SHA256() != "" {
		return current.cursor, ErrConflict
	}
	raw, err := json.Marshal(struct {
		Scope           datapublish.ProofScope `json:"scope"`
		ManifestSHA256  string                 `json:"manifest_sha256"`
		TransportSHA256 string                 `json:"transport_sha256"`
		EvidenceSHA256  string                 `json:"evidence_sha256"`
		VerifiedAt      time.Time              `json:"verified_at"`
	}{current.cursor.Scope(), bundle.manifest.SHA256(), current.point.view.TransportPointSHA256, evidence.SHA256(), restored.VerifiedAt()})
	if err != nil {
		return current.cursor, ErrInvalid
	}
	observation := publicationOriginalObservation(current.cursor, current.point.view, datapublish.RestoreStage, publicationStorageSHA256(raw))
	next, err := current.cursor.Observe(observation)
	if err != nil {
		return current.cursor, ErrConflict
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO data_module_publication_proofs VALUES(?,?,'staging_restore',?,?)", current.review.Plan().ID(), "restore", publicationStorageSHA256(raw), raw); err != nil {
		return current.cursor, err
	}
	if err = appendPublicationCursorTx(ctx, tx, current.review.Plan(), current.cursor, next); err != nil {
		return current.cursor, err
	}
	if err = tx.Commit(); err != nil {
		return current.cursor, err
	}
	identity, err := restored.Root().Stat(".")
	if err != nil {
		return next, err
	}
	attempt.restored = &publicationReviewRestore{root: restored.Root(), parent: parent, identity: identity, verifiedAt: restored.VerifiedAt(), sourceEvidenceSHA: evidence.SHA256(), download: restored}
	return next, nil
}

func publicationOriginalObservation(cursor datapublish.Cursor, point NativePreparedPointView, step datapublish.Step, proofSHA string) datapublish.Observation {
	return datapublish.Observation{Scope: cursor.Scope(), Step: step, Outcome: datapublish.ObservedCompleted, ProofSHA256: proofSHA, NativeDatabase: point.NativeDatabase, NativePublisher: point.NativePublisher, ProgramWireHash: point.ProgramWireHash, SchemaSHA256: point.SchemaSHA256, Requirements: []datapublish.RequirementProof{}}
}
