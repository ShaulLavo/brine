package store

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/datapublish"
	"github.com/ShaulLavo/brine/internal/restic"
)

// PublicationTransition retains the actual 18 attempt, 24 target intent and
// lifetime guard while the existing engine transition journal moves the trees.
type PublicationTransition struct {
	preparation      *PublicationTargetPreparation
	inputSHA256      string
	retainedResolver bool
}

func (PublicationTransition) MarshalJSON() ([]byte, error) { return nil, ErrInvalid }
func (PublicationTransition) String() string               { return "[private publication transition]" }
func (PublicationTransition) GoString() string             { return "[private publication transition]" }

func (p *PublicationTransition) active(ctx context.Context, s *Store) bool {
	return p != nil && p.preparation.active(s) && data.ValidCatalogHash(p.inputSHA256) && (p.retainedResolver && p.preparation.attempt.resolverID != "" || p.preparation.attempt.restored.unchanged(ctx) == nil)
}

type PublicationTransitionView struct {
	Input      data.EngineTransition
	Cursor     data.EngineTransitionCursor
	Runtime    data.EngineRuntime
	Allocation data.AllocationReceipt
}

func (s *Store) ClaimPublicationTransition(ctx context.Context, capability *PublicationTargetPreparation, incoming *NativeIncomingRepository, initialized restic.InitializedRepository) (*PublicationTransition, error) {
	if !capability.active(s) || !incoming.active(s) || capability.attempt.restored.unchanged(ctx) != nil {
		return nil, ErrConflict
	}
	tx, cancel, err := s.beginWrite(ctx)
	defer cancel()
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	current, _, _, _, err := s.readPublicationTargetPreparationTx(ctx, tx, capability)
	if err != nil {
		return nil, err
	}
	intent, err := s.readNativeIncomingRepositoryTx(ctx, tx, incoming)
	if err != nil || intent.creationID != current.claim.RepositoryIntentID || intent.hash != current.claim.RepositoryIntentSHA || intent.record.OperationID != current.claim.ApplyOpID || intent.epochID != current.claim.NewEpochID || intent.record.ConsumerSHA256 != current.claim.ConsumerSHA {
		return nil, errors.Join(ErrConflict, err)
	}
	location := restic.S3Location{Endpoint: intent.archive.Destination.Endpoint, Bucket: intent.archive.Destination.Bucket, Prefix: intent.archive.RemotePrefix, Region: intent.archive.Destination.Region, PathStyle: intent.archive.Destination.PathStyle}
	scope := initialized.Scope()
	scope.RepositoryID = ""
	if scope.BindingID != string(current.claim.BindingID) || scope.EpochID != string(current.claim.NewEpochID) || scope.DestinationSHA256 != location.SHA256() || !initialized.AdmitsCreation(scope, intent.creationGrantSHA256) {
		return nil, ErrConflict
	}
	var repositoryID string
	if err = tx.QueryRowContext(ctx, "SELECT repository_id FROM data_native_repository_results WHERE intent_id=?", intent.creationID).Scan(&repositoryID); err != nil || repositoryID != initialized.Scope().RepositoryID {
		return nil, errors.Join(ErrConflict, err)
	}
	target, err := readPublicationTargetTx(ctx, tx, current)
	if err != nil {
		return nil, err
	}
	input, err := publicationTransitionInput(ctx, tx, current, target)
	if err != nil {
		return nil, err
	}
	if _, err = s.insertEngineTransitionTx(ctx, tx, capability.attempt.preparation, input, repositoryID); err != nil {
		return nil, err
	}
	next, err := current.cursor.Intend(datapublish.FinalTransition)
	if err != nil {
		return nil, ErrConflict
	}
	if err = appendPublicationCursorTx(ctx, tx, current.review.Plan(), current.cursor, next); err != nil {
		return nil, err
	}
	transition := &PublicationTransition{preparation: capability, inputSHA256: input.SHA256()}
	if !transition.active(ctx, s) {
		return nil, ErrConflict
	}
	return transition, tx.Commit()
}

func publicationTransitionInput(ctx context.Context, tx *sql.Tx, current publicationAttemptState, target publicationTarget) (data.EngineTransition, error) {
	plan, point, claim := current.review.Plan(), current.point.view, current.claim
	facts := plan.Facts()
	scope := data.EngineTransitionScope{ID: claim.RepositoryIntentID, ApplyOperationID: claim.ApplyOpID, PlanID: plan.ID(), Requester: plan.Requester().String(), PolicyHash: "sha256:" + facts.PolicySHA256, TargetHash: "sha256:" + facts.TargetSHA256, DesiredHash: "sha256:" + facts.DesiredSHA256, Generation: facts.Generation, IncarnationID: point.IncarnationID, DatabaseID: point.DatabaseID, BindingID: point.BindingID, ExpiresAt: plan.ExpiresAt()}
	ref := func(kind data.EngineEvidenceKind, id, hash string, epoch data.ReplicaEpochID) data.EngineEvidenceRef {
		return data.EngineEvidenceRef{Kind: kind, ID: id, SHA256: hash, DatabaseID: scope.DatabaseID, BindingID: scope.BindingID, EpochID: epoch}
	}
	head := data.EngineHeadRef{BindingID: point.BindingID, EpochID: point.EpochID, Version: point.RuntimeVersion, RuntimeSHA256: point.RuntimeSHA256}
	base, err := data.CommittedEngineBase(head)
	if plan.Source().Kind() == datapublish.CreateSource {
		base, err = data.UnpublishedEngineBase(data.EngineUnpublishedBase{Head: head, Allocation: ref(data.EngineAllocationEvidence, point.AllocationID, point.AllocationSHA256, point.EpochID)})
	}
	if err != nil {
		return data.EngineTransition{}, err
	}
	bundle, err := readNativePreparedBundleTx(ctx, tx, point.PointID)
	if err != nil {
		return data.EngineTransition{}, err
	}
	sourceRaw, err := json.Marshal(bundle.source)
	if err != nil {
		return data.EngineTransition{}, err
	}
	dependencies := publicationStorageSHA256(bytes.Join([][]byte{sourceRaw, bundle.manifest.Bytes(), []byte(point.TransportPointSHA256), []byte(point.SigningRecoverySHA256)}, []byte{0}))
	recovery, _ := json.Marshal(plan.Review().Recovery)
	_, publicationSHA, err := readPublicationPublishedTx(ctx, tx, current.cursor)
	if err != nil {
		return data.EngineTransition{}, err
	}
	r := target.record
	common := data.EngineTransitionCommon{Engine: data.EngineSpacetimeDB, Scope: scope, Base: base, NewEpochID: claim.NewEpochID, FenceID: point.FenceID, FenceOwnerOperationID: point.FenceOwnerOpID, PreparedPoint: ref(data.EnginePreparedPointEvidence, point.PointID, current.point.hash, point.EpochID), SourceWitness: ref(data.EngineSourceWitnessEvidence, point.PointID, point.SourceWitnessSHA256, point.EpochID), OldTree: r.OldTree, TargetTree: r.TargetTree, PreservedParent: r.TargetTree.Parent, TargetVerification: ref(data.EngineTargetEvidence, claim.RepositoryIntentID, target.hash, claim.NewEpochID), TargetRuntimeSHA256: r.RuntimeSHA256, TargetCheckpoint: ref(data.EngineCheckpointEvidence, claim.RepositoryIntentID, r.CheckpointSHA256, claim.NewEpochID), TargetMaterial: ref(data.EngineMaterialEvidence, claim.RepositoryIntentID, r.MaterialSHA256, claim.NewEpochID), RecoveryRelease: ref(data.EngineRecoveryEvidence, point.PointID, publicationStorageSHA256(recovery), claim.NewEpochID), RetainedDependencies: ref(data.EngineDependenciesEvidence, point.PointID, dependencies, claim.NewEpochID)}
	return data.NewEnginePublicationTransition(common, data.EnginePublicationTarget{CandidateSHA256: plan.Candidate().WasmSHA256(), Receipt: ref(data.EnginePublicationEvidence, claim.RepositoryIntentID, publicationSHA, claim.NewEpochID)})
}

func (s *Store) ReadPublicationTransition(ctx context.Context, capability *PublicationTransition) (PublicationTransitionView, error) {
	if !capability.active(ctx, s) {
		return PublicationTransitionView{}, ErrConflict
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return PublicationTransitionView{}, err
	}
	defer func() { _ = tx.Rollback() }()
	_, target, input, cursor, err := s.readPublicationTransitionTx(ctx, tx, capability)
	if err != nil {
		return PublicationTransitionView{}, err
	}
	view := PublicationTransitionView{Input: input, Cursor: cursor, Runtime: target.runtime.Clone(), Allocation: cloneNativeAllocation(target.record.Allocation)}
	return view, tx.Commit()
}

func (s *Store) readPublicationTransitionTx(ctx context.Context, tx *sql.Tx, capability *PublicationTransition) (publicationAttemptState, publicationTarget, data.EngineTransition, data.EngineTransitionCursor, error) {
	var target publicationTarget
	var input data.EngineTransition
	var cursor data.EngineTransitionCursor
	if !capability.active(ctx, s) {
		return publicationAttemptState{}, target, input, cursor, ErrConflict
	}
	current, err := s.readPublicationAttemptTx(ctx, tx, capability.preparation.attempt)
	if err != nil {
		return current, target, input, cursor, err
	}
	if current.cursor.IntendedStep() != datapublish.FinalTransition {
		return current, target, input, cursor, ErrConflict
	}
	if err = checkPublicationTransitionReviewTx(ctx, tx, current); err != nil {
		return current, target, input, cursor, err
	}
	target, err = readPublicationTargetTx(ctx, tx, current)
	if err != nil {
		return current, target, input, cursor, err
	}
	expected, err := publicationTransitionInput(ctx, tx, current, target)
	if err != nil {
		return current, target, input, cursor, err
	}
	input, cursor, err = readEngineTransition(ctx, tx, current.claim.RepositoryIntentID)
	if err != nil || input.SHA256() != expected.SHA256() || input.SHA256() != capability.inputSHA256 || checkEngineTransitionFence(ctx, tx, input) != nil || checkTransitionRuntimeStage(ctx, tx, input, cursor) != nil {
		return current, target, input, cursor, errors.Join(ErrConflict, err)
	}
	return current, target, input, cursor, nil
}

func checkPublicationTransitionReviewTx(ctx context.Context, tx *sql.Tx, current publicationAttemptState) error {
	if err := checkPublicationPolicy(ctx, tx, current.review.Plan().Facts()); err != nil {
		return err
	}
	approval, err := readPublicationReviewApprovalTx(ctx, tx, current.review, current.index)
	if err != nil {
		return err
	}
	_, desired, err := loadPlan(ctx, tx, approval.TargetDeclarationID)
	if err != nil || desired.SpacetimeDB == nil {
		return errors.Join(ErrConflict, err)
	}
	recovery := current.review.Plan().Review().Recovery
	ids := make([]string, 0, len(recovery.Releases()))
	for _, release := range recovery.Releases() {
		ids = append(ids, release.ReleaseID)
	}
	actual, err := readPublicationRecoveryTx(ctx, tx, current.review.Plan().Facts().App, current.point.view, desired, ids)
	a, _ := json.Marshal(actual)
	b, _ := json.Marshal(recovery)
	if err != nil || !bytes.Equal(a, b) {
		return errors.Join(ErrConflict, err)
	}
	return nil
}
