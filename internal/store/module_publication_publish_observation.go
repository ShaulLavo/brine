package store

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"

	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/datapublish"
	"github.com/ShaulLavo/brine/internal/spacetimedb"
)

func (s *Store) RecordPublicationPublished(ctx context.Context, attempt *PublicationAttempt, observed PublicationStageObservation) (datapublish.Cursor, error) {
	tx, cancel, err := s.beginWrite(ctx)
	defer cancel()
	if err != nil {
		return datapublish.Cursor{}, err
	}
	defer func() { _ = tx.Rollback() }()
	current, err := s.readPublicationAttemptTx(ctx, tx, attempt)
	if err != nil || current.cursor.State() != datapublish.IntentRecorded || current.cursor.IntendedStep() != datapublish.PublishStage {
		return datapublish.Cursor{}, errors.Join(ErrConflict, err)
	}
	approval, err := readPublicationReviewApprovalTx(ctx, tx, current.review, current.index)
	if err != nil || !data.ValidCatalogHash(observed.NativeDatabase) || !data.ValidCatalogHash(observed.NativePublisher) || observed.ProgramWireHash != current.review.Plan().Candidate().ProgramWireHash() || observed.SchemaSHA256 != current.review.Plan().Candidate().Schema().SHA256() || !data.ValidCatalogHash(observed.ReceiptBindingSHA256) || len(observed.Receipt) == 0 || len(observed.Receipt) > spacetimedb.NativePublicationResponseLimit {
		return current.cursor, errors.Join(ErrConflict, err)
	}
	process := observed.Process
	if process.Publisher != observed.NativePublisher || process.HelperPID <= 1 || process.HelperStartTicks == 0 || process.HelperCgroup == "" || process.ChildPID <= 1 || process.ChildStartTicks == 0 || !process.Disposed || process.StartedAt.Before(approval.RestoreVerifiedAt) || checkNativeInitialPublisherTranscript(NativeInitialOwnerObservation{Publisher: process.Publisher, CredentialSHA256: process.Runtime.Secrets.Publisher.SHA256, PublisherProof: process.PublisherProof, StartedAt: process.StartedAt, FinishedAt: process.FinishedAt}) != nil {
		return current.cursor, ErrConflict
	}
	permit, err := readEnginePermit(ctx, tx, current.point.view.BindingID)
	actual, _ := json.Marshal(permit.Runtime)
	expected, _ := json.Marshal(process.Runtime)
	if err != nil || !bytes.Equal(actual, expected) {
		return current.cursor, errors.Join(ErrConflict, err)
	}
	if current.point.view.SourceKind == "initialized" {
		if observed.NativeDatabase != current.point.view.NativeDatabase || observed.NativePublisher != current.point.view.NativePublisher || !bytes.Equal(process.Preflight, current.review.NativePreflightResponse()) || spacetimedb.CheckNativeUpdateResponse(observed.Receipt, spacetimedb.DatabaseIdentity(observed.NativeDatabase)) != nil {
			return current.cursor, ErrConflict
		}
	} else {
		database, err := spacetimedb.NativeCreatedDatabase(observed.Receipt, current.review.Plan().CreationName())
		if err != nil || string(database) != observed.NativeDatabase || len(process.Preflight) != 0 || observed.NativePublisher != approval.Observation.Publisher {
			return current.cursor, ErrConflict
		}
	}
	raw, err := json.Marshal(observed)
	if err != nil || len(raw) > 4<<20 {
		return current.cursor, ErrInvalid
	}
	hash := publicationStorageSHA256(raw)
	proof := datapublish.Observation{Scope: current.cursor.Scope(), Step: datapublish.PublishStage, Outcome: datapublish.ObservedCompleted, ProofSHA256: hash, NativeDatabase: observed.NativeDatabase, NativePublisher: observed.NativePublisher, ProgramWireHash: observed.ProgramWireHash, SchemaSHA256: observed.SchemaSHA256, Requirements: []datapublish.RequirementProof{}}
	next, err := current.cursor.Observe(proof)
	if err != nil {
		return current.cursor, ErrConflict
	}
	if err = insertPublicationProofTx(ctx, tx, current.review.Plan().ID(), "publication", publicationReceiptProof, raw); err != nil {
		return current.cursor, err
	}
	if err = appendPublicationCursorTx(ctx, tx, current.review.Plan(), current.cursor, next); err != nil {
		return current.cursor, err
	}
	return next, tx.Commit()
}
