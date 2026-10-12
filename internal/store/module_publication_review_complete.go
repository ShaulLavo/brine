package store

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/datainit"
	"github.com/ShaulLavo/brine/internal/datapublish"
	"github.com/ShaulLavo/brine/internal/spacetimedb"
	"github.com/ShaulLavo/brine/internal/strictjson"
)

type publicationReviewApproval struct {
	Format                string                       `json:"format"`
	PlanID                string                       `json:"plan_id"`
	Requester             string                       `json:"requester"`
	PointID               string                       `json:"point_id"`
	PointSHA256           string                       `json:"point_sha256"`
	TargetDeclarationID   string                       `json:"target_declaration_id"`
	RestoreVerifiedAt     time.Time                    `json:"restore_verified_at"`
	RestoreEvidenceSHA256 string                       `json:"restore_evidence_sha256"`
	ReviewedAt            time.Time                    `json:"reviewed_at"`
	ExpiresAt             time.Time                    `json:"expires_at"`
	Observation           PublicationReviewObservation `json:"observation"`
}

func (s *Store) CompletePublicationReview(ctx context.Context, preparation *PublicationReviewPreparation, observed PublicationReviewObservation) (datapublish.Plan, error) {
	if !preparation.active(s) || preparation.declaration == nil || preparation.restore.unchanged(ctx) != nil {
		return datapublish.Plan{}, ErrConflict
	}
	tx, cancel, err := s.beginWrite(ctx)
	defer cancel()
	if err != nil {
		return datapublish.Plan{}, err
	}
	defer func() { _ = tx.Rollback() }()
	point, err := s.readPublicationReviewPreparationTx(ctx, tx, preparation)
	if err != nil {
		return datapublish.Plan{}, err
	}
	declaration, err := readPublicationReviewDeclarationTx(ctx, tx, preparation, point, preparation.declaration.request)
	if err != nil || !samePublicationDeclaration(*preparation.declaration, declaration) {
		return datapublish.Plan{}, errors.Join(ErrConflict, err)
	}
	review, err := buildObservedPublicationReview(ctx, tx, preparation, point, declaration, observed)
	if err != nil {
		return datapublish.Plan{}, err
	}
	if err = recordObservedPublicationReviewTx(ctx, tx, preparation, point, review, observed); err != nil {
		return datapublish.Plan{}, err
	}
	if !preparation.active(s) || preparation.restore.unchanged(ctx) != nil {
		return datapublish.Plan{}, ErrConflict
	}
	return review.Plan(), tx.Commit()
}

func samePublicationDeclaration(old, current publicationReviewDeclaration) bool {
	a, err := json.Marshal(struct {
		Facts    datapublish.BindingFacts
		Recovery datapublish.RecoveryReview
		Target   string
	}{old.facts, old.recovery, old.plan.Hash})
	b, currentErr := json.Marshal(struct {
		Facts    datapublish.BindingFacts
		Recovery datapublish.RecoveryReview
		Target   string
	}{current.facts, current.recovery, current.plan.Hash})
	return err == nil && currentErr == nil && bytes.Equal(a, b)
}

func buildObservedPublicationReview(ctx context.Context, tx *sql.Tx, preparation *PublicationReviewPreparation, point nativePreparedPoint, declaration publicationReviewDeclaration, observed PublicationReviewObservation) (datapublish.PrivateReview, error) {
	schema, err := spacetimedb.DecodeDescriptorV10(observed.Descriptor, spacetimedb.DefaultSchemaLimits())
	metadata := declaration.desired.SpacetimeDB.Module.Metadata
	if err != nil || schema.SHA256() != metadata.SchemaSHA256 || schema.Version() != metadata.Canonicalizer || !observedPublicationDescriptorMatches(observed.Extraction, observed.Runtime) {
		return datapublish.PrivateReview{}, ErrConflict
	}
	permit, err := readEnginePermit(ctx, tx, point.view.BindingID)
	actualRuntime, _ := json.Marshal(permit.Runtime)
	observedRuntime, _ := json.Marshal(observed.Runtime)
	if err != nil || !bytes.Equal(actualRuntime, observedRuntime) {
		return datapublish.PrivateReview{}, errors.Join(ErrConflict, err)
	}
	candidate, err := datapublish.NewCandidate(string(preparation.artifact.WasmSHA256()), string(preparation.artifact.ProgramWireHash()), schema)
	if err != nil {
		return datapublish.PrivateReview{}, err
	}
	if err = checkPublicationCandidateRegistry(ctx, tx, point.view.DatabaseID, declaration.desired.SpacetimeDB.Module.Marker, candidate); err != nil {
		return datapublish.PrivateReview{}, err
	}
	source, err := publicationSourceFromPoint(point.view, declaration.bundle.Schema())
	if err != nil {
		return datapublish.PrivateReview{}, err
	}
	requester, err := datainit.ParseRequester(preparation.requester)
	if err != nil {
		return datapublish.PrivateReview{}, err
	}
	now := time.Now().UTC()
	expires := now.Add(datapublish.MaxReviewLifetime)
	if expires.After(point.view.ExpiresAt) {
		expires = point.view.ExpiresAt
	}
	spec := datapublish.PlanSpec{Requester: requester, Facts: declaration.facts, Source: source, Candidate: candidate, Review: datapublish.ReviewReferences{GuestCodeSHA256: candidate.WasmSHA256(), Recovery: declaration.recovery}, IssuedAt: now, ExpiresAt: expires}
	var sourceSchema, token []byte
	if source.Kind() == datapublish.CreateSource {
		if len(observed.Preflight) != 0 || observed.Runtime.State != data.EngineUnpublished || checkObservedInitialReviewOwner(ctx, tx, point, observed) != nil {
			return datapublish.PrivateReview{}, ErrConflict
		}
		spec.CreationName = declaration.desired.SpacetimeDB.Database
	} else {
		if !observedPublicationPreflightMatches(observed, preparation.restore.verifiedAt) {
			return datapublish.PrivateReview{}, ErrConflict
		}
		parsed, err := spacetimedb.DescribeNativePreflight(observed.Preflight)
		if err != nil || !parsed.Automatic() || parsed.MajorUpgrade() {
			return datapublish.PrivateReview{}, ErrConflict
		}
		ref, err := data.NewID()
		if err != nil {
			return datapublish.PrivateReview{}, err
		}
		spec.Preflight = datapublish.NativePreflight{ResponseSHA256: parsed.ResponseSHA256(), TokenSHA256: parsed.TokenSHA256(), PrivateTokenRef: ref}
		token = parsed.TokenBytes()
		descriptor, err := readNativePreparedDescriptorTx(ctx, tx, declaration.bundle)
		if err != nil {
			return datapublish.PrivateReview{}, err
		}
		fields, err := strictjson.Object(descriptor, "V10")
		if err != nil {
			return datapublish.PrivateReview{}, ErrConflict
		}
		sourceSchema = fields["V10"]
	}
	planned, err := datapublish.NewPlan(spec)
	if err != nil {
		return datapublish.PrivateReview{}, err
	}
	return datapublish.NewPrivateReview(planned, preparation.artifact, sourceSchema, observed.Descriptor, observed.Preflight, token)
}

func publicationSourceFromPoint(point NativePreparedPointView, schema spacetimedb.CanonicalSchema) (datapublish.Source, error) {
	if point.SourceKind == "unpublished" {
		return datapublish.NewEmptySource(datapublish.EmptySource{AllocationID: point.AllocationID, PointID: point.PointID, FenceID: string(point.FenceID), PreparationOpID: point.PreparationOperationID,
			RestoreReceiptSHA256: point.RestoreReceiptSHA256, BundleManifestSHA256: point.BundleManifestSHA256, SourceSealSHA256: point.SourceSealSHA256, ConfigSHA256: point.ConfigSHA256, SigningRecoverySHA256: point.SigningRecoverySHA256})
	}
	return datapublish.NewCapturedSource(datapublish.CapturedPoint{PointID: point.PointID, FenceID: string(point.FenceID), PreparationOpID: point.PreparationOperationID, RestoreReceiptSHA256: point.RestoreReceiptSHA256, BundleManifestSHA256: point.BundleManifestSHA256,
		SourceSealSHA256: point.SourceSealSHA256, StateWitnessSHA256: point.SourceWitnessSHA256, StopProofSHA256: point.StopProofSHA256, CheckpointSHA256: point.CheckpointSHA256, NativeDatabase: point.NativeDatabase, NativePublisher: point.NativePublisher, ProgramWireHash: point.ProgramWireHash, WasmSHA256: point.ArtifactWasmSHA256, Names: point.Names}, schema)
}

func observedPublicationDescriptorMatches(v PublicationDescriptorObservation, runtime data.EngineRuntime) bool {
	return v.Image == runtime.Image && v.ManifestDigest == runtime.ManifestDigest && v.ImageID == runtime.ImageID && v.HelperPID > 1 && v.HelperStartTicks > 0 && v.HelperCgroup != "" && v.Disposed && !v.StartedAt.IsZero() && v.StartedAt.Location() == time.UTC && v.FinishedAt.Location() == time.UTC && !v.FinishedAt.Before(v.StartedAt) && !v.FinishedAt.After(time.Now().UTC())
}

func readNativePreparedDescriptorTx(ctx context.Context, tx *sql.Tx, bundle NativePreparedBundle) ([]byte, error) {
	checkpoint, _, initialized := bundle.source.Initialized()
	if !initialized {
		return nil, ErrConflict
	}
	c := checkpoint.Record()
	var descriptor []byte
	if err := tx.QueryRowContext(ctx, "SELECT descriptor FROM data_native_checkpoints WHERE binding_id=? AND epoch_id=? AND generation_id=?", c.Scope.BindingID, c.Scope.EpochID, c.GenerationID).Scan(&descriptor); err != nil {
		return nil, err
	}
	schema, err := spacetimedb.DecodeDescriptorV10(descriptor, spacetimedb.DefaultSchemaLimits())
	if err != nil || schema.SHA256() != bundle.schema.SHA256() {
		return nil, &IntegrityError{}
	}
	return descriptor, nil
}

func checkObservedInitialReviewOwner(ctx context.Context, tx *sql.Tx, point nativePreparedPoint, observed PublicationReviewObservation) error {
	var raw []byte
	var owner NativeInitialOwnerObservation
	if err := tx.QueryRowContext(ctx, "SELECT canonical FROM data_native_initial_owners WHERE point_id=?", point.view.PointID).Scan(&raw); err != nil {
		return err
	}
	if json.Unmarshal(raw, &owner) != nil || nativePointHash(raw) != point.view.SourceWitnessSHA256 || owner.PointID != point.view.PointID || owner.SourceSHA256 != point.view.SourceSHA256 || owner.SourceSealSHA256 != point.view.SourceSealSHA256 || owner.GenerationID != point.view.GenerationID || owner.Publisher != observed.Publisher || !bytes.Equal(owner.PublisherProof, observed.PublisherProof) || checkNativeInitialPublisherTranscript(owner) != nil {
		return &IntegrityError{}
	}
	return nil
}

func observedPublicationPreflightMatches(v PublicationReviewObservation, restoredAt time.Time) bool {
	if v.Runtime.State != data.EngineInitialized || v.Publisher != string(v.Runtime.Publisher) || v.HelperPID <= 1 || v.HelperStartTicks == 0 || v.HelperCgroup == "" || v.ChildPID <= 1 || v.ChildStartTicks == 0 || !v.Disposed || v.StartedAt.Before(restoredAt) || v.FinishedAt.After(time.Now().UTC()) {
		return false
	}
	proof := NativeInitialOwnerObservation{Publisher: v.Publisher, CredentialSHA256: v.Runtime.Secrets.Publisher.SHA256, PublisherProof: v.PublisherProof, StartedAt: v.StartedAt, FinishedAt: v.FinishedAt}
	return checkNativeInitialPublisherTranscript(proof) == nil
}

func recordObservedPublicationReviewTx(ctx context.Context, tx *sql.Tx, preparation *PublicationReviewPreparation, point nativePreparedPoint, review datapublish.PrivateReview, observed PublicationReviewObservation) error {
	raw, err := review.StorageBytes()
	if err != nil {
		return ErrInvalid
	}
	p := point.view
	index := publicationReviewBinding{pointID: p.PointID, binding: p.BindingID, requester: preparation.requester, generation: p.Generation, generationID: p.GenerationID, pointSHA: point.hash, runtimeVersion: p.RuntimeVersion, runtimeSHA: p.RuntimeSHA256}
	if err = insertPublicationReview(ctx, tx, review.Plan().ID(), index, raw); err != nil {
		return err
	}
	approval := publicationReviewApproval{Format: "brine-publication-review-approval-v1", PlanID: review.Plan().ID(), Requester: preparation.requester, PointID: p.PointID, PointSHA256: point.hash, TargetDeclarationID: preparation.declaration.plan.Hash,
		RestoreVerifiedAt: preparation.restore.verifiedAt, RestoreEvidenceSHA256: preparation.restore.sourceEvidenceSHA, ReviewedAt: time.Now().UTC(), ExpiresAt: review.Plan().ExpiresAt(), Observation: observed}
	encoded, err := json.Marshal(approval)
	if err != nil || len(encoded) > 4<<20 {
		return ErrInvalid
	}
	_, err = tx.ExecContext(ctx, "INSERT INTO data_module_publication_approvals VALUES(?,?,?,?,?,?)", approval.PlanID, approval.Requester, approval.PointID, approval.TargetDeclarationID, publicationStorageSHA256(encoded), encoded)
	return err
}
