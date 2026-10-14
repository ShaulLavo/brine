package store

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"time"

	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/datapublish"
	"github.com/ShaulLavo/brine/internal/nativeevidence"
	"github.com/ShaulLavo/brine/internal/restic"
)

// PublicationPrivateInspection holds only an original resolver claim before
// live transition admission. It grants inspection, never another native effect.
type PublicationPrivateInspection struct {
	state    *Store
	attempt  *PublicationAttempt
	lifetime *data.EngineLock
}

func (PublicationPrivateInspection) MarshalJSON() ([]byte, error) { return nil, ErrInvalid }
func (p *PublicationPrivateInspection) active(s *Store) bool {
	return p != nil && p.state == s && p.attempt.active(s) && p.attempt.resolverID != "" && p.lifetime.HoldsLifetime(s.dir, p.attempt.binding)
}

type PublicationPrivateInspectionFacts struct {
	Point           NativePreparedPointView
	Permit          EnginePermit
	Effects         []PublicationEffect
	Manifest        restic.Manifest
	Owner           string
	IncomingEpochID data.ReplicaEpochID
}

func (PublicationPrivateInspectionFacts) MarshalJSON() ([]byte, error) { return nil, ErrInvalid }

func (s *Store) BeginPublicationPrivateInspection(ctx context.Context, preparation *EnginePreparation, lifetime *data.EngineLock, planID, originalID, resolverID string) (*PublicationPrivateInspection, error) {
	attempt, err := s.ResumePublicationAttempt(ctx, preparation, planID, originalID, resolverID)
	if err != nil {
		return nil, err
	}
	capability := &PublicationPrivateInspection{state: s, attempt: attempt, lifetime: lifetime}
	if _, err = s.ReadPublicationPrivateInspectionFacts(ctx, capability); err != nil {
		return nil, err
	}
	return capability, nil
}

func (s *Store) ReadPublicationPrivateInspectionFacts(ctx context.Context, capability *PublicationPrivateInspection) (PublicationPrivateInspectionFacts, error) {
	if !capability.active(s) {
		return PublicationPrivateInspectionFacts{}, ErrConflict
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return PublicationPrivateInspectionFacts{}, err
	}
	defer func() { _ = tx.Rollback() }()
	current, permit, effects, bundle, owner, err := s.readPublicationPrivateInspectionTx(ctx, tx, capability)
	if err != nil {
		return PublicationPrivateInspectionFacts{}, err
	}
	point := current.point.view
	point.PointSHA256 = current.point.hash
	facts := PublicationPrivateInspectionFacts{Point: point, Permit: permit, Effects: effects, Manifest: bundle.manifest, Owner: owner, IncomingEpochID: current.claim.NewEpochID}
	return facts, tx.Commit()
}

func (s *Store) readPublicationPrivateInspectionTx(ctx context.Context, tx *sql.Tx, capability *PublicationPrivateInspection) (publicationAttemptState, EnginePermit, []PublicationEffect, NativePreparedBundle, string, error) {
	var permit EnginePermit
	var bundle NativePreparedBundle
	var effects []PublicationEffect
	current, err := s.readPublicationAttemptTx(ctx, tx, capability.attempt)
	if err != nil || current.cursor.State() == datapublish.Finished || current.cursor.State() == datapublish.SourcePreserved {
		return current, permit, effects, bundle, "", errors.Join(ErrConflict, err)
	}
	if _, _, err = readEngineTransition(ctx, tx, current.claim.RepositoryIntentID); !errors.Is(err, ErrNotFound) {
		return current, permit, effects, bundle, "", errors.Join(ErrConflict, err)
	}
	if err = checkPublicationTransitionReviewTx(ctx, tx, current); err != nil {
		return current, permit, effects, bundle, "", err
	}
	var held, transitions int
	err = tx.QueryRowContext(ctx, "SELECT count(*) FROM data_fences f JOIN data_active_incarnations a ON a.incarnation_id=f.incarnation_id WHERE f.id=? AND f.operation_id=? AND f.database_id=? AND f.incarnation_id=? AND f.state='held'", current.point.view.FenceID, current.point.view.FenceOwnerOpID, current.point.view.DatabaseID, current.point.view.IncarnationID).Scan(&held)
	if err != nil || held != 1 {
		return current, permit, effects, bundle, "", errors.Join(ErrConflict, err)
	}
	if err = tx.QueryRowContext(ctx, "SELECT count(*) FROM data_engine_transition_inputs WHERE fence_id=?", current.point.view.FenceID).Scan(&transitions); err != nil || transitions != 0 {
		return current, permit, effects, bundle, "", errors.Join(ErrConflict, err)
	}
	permit, err = readPublicationSourcePermitTx(ctx, tx, current)
	if err != nil {
		return current, permit, effects, bundle, "", err
	}
	head, _, err := readEngineHead(ctx, tx, current.claim.BindingID)
	if err != nil || head.EpochID != current.claim.OldEpochID || head.Version != current.index.runtimeVersion || head.RuntimeSHA256 != current.index.runtimeSHA {
		return current, permit, effects, bundle, "", errors.Join(ErrConflict, err)
	}
	bundle, err = readNativePreparedBundleTx(ctx, tx, current.point.view.PointID)
	if err != nil {
		return current, permit, effects, bundle, "", err
	}
	approval, err := readPublicationReviewApprovalTx(ctx, tx, current.review, current.index)
	if err != nil {
		return current, permit, effects, bundle, "", err
	}
	owner := approval.Observation.Publisher
	if !data.ValidCatalogHash(owner) {
		return current, permit, effects, bundle, "", ErrConflict
	}
	rows, err := tx.QueryContext(ctx, "SELECT nonce FROM data_module_publication_effects WHERE plan_id=? AND operation_id=? ORDER BY rowid", current.claim.PlanID, current.claim.ApplyOpID)
	if err != nil {
		return current, permit, effects, bundle, "", err
	}
	var nonces []string
	for rows.Next() {
		var nonce string
		if err = rows.Scan(&nonce); err != nil || len(nonces) >= 2 {
			_ = rows.Close()
			return current, permit, effects, bundle, "", errors.Join(ErrConflict, err)
		}
		nonces = append(nonces, nonce)
	}
	if err = errors.Join(rows.Err(), rows.Close()); err != nil {
		return current, permit, effects, bundle, "", err
	}
	for _, nonce := range nonces {
		effect, err := readPublicationEffectTx(ctx, tx, nonce)
		if err != nil || effect.Scope != current.cursor.Scope() || effect.Step != datapublish.CheckPreflight && effect.Step != datapublish.PublishStage {
			return current, permit, effects, bundle, "", errors.Join(ErrConflict, err)
		}
		if _, err = readPublicationEffectEventsTx(ctx, tx, effect); err != nil {
			return current, permit, effects, bundle, "", err
		}
		if len(effects) != 0 && (effect.RootPath != effects[0].RootPath || effect.RootIdentity != effects[0].RootIdentity) {
			return current, permit, effects, bundle, "", ErrConflict
		}
		effects = append(effects, effect)
	}
	return current, permit, effects, bundle, owner, nil
}

type PublicationSourcePreservationObservation struct {
	OriginalRoot         *os.Root
	OriginalSealSHA256   string
	WorkingRoot          *os.Root
	WorkingEvidence      nativeevidence.Evidence
	ReaderNonce          string
	ReaderViewSHA256     string
	ReaderImageID        string
	ReaderDisposedAt     time.Time
	EngineAbsent         bool
	ApplicationAbsent    bool
	IngressClosed        bool
	PrivateReadersAbsent bool
	ObservedAt           time.Time
}

func (PublicationSourcePreservationObservation) MarshalJSON() ([]byte, error) { return nil, ErrInvalid }

type publicationSourcePreservationRecord struct {
	Format                string                 `json:"format"`
	Scope                 datapublish.ProofScope `json:"scope"`
	ResolverID            string                 `json:"resolver_id"`
	OriginalOutcomeSHA256 string                 `json:"original_outcome_sha256"`
	OriginalSealSHA256    string                 `json:"original_seal_sha256"`
	WorkingKind           string                 `json:"working_kind"`
	WorkingEvidence       json.RawMessage        `json:"working_evidence"`
	ModuleSHA256          string                 `json:"module_sha256"`
	SchemaSHA256          string                 `json:"schema_sha256"`
	ReaderNonce           string                 `json:"reader_nonce"`
	ReaderViewSHA256      string                 `json:"reader_view_sha256"`
	ReaderImageID         string                 `json:"reader_image_id"`
	ReaderDisposedAt      time.Time              `json:"reader_disposed_at"`
	ObservedAt            time.Time              `json:"observed_at"`
}

func (s *Store) PreservePublicationOriginalSource(ctx context.Context, capability *PublicationPrivateInspection, observed PublicationSourcePreservationObservation) (datapublish.TaskReceipt, error) {
	zero := datapublish.TaskReceipt{}
	if !capability.active(s) {
		return zero, ErrConflict
	}
	tx, cancel, err := s.beginWrite(ctx)
	defer cancel()
	if err != nil {
		return zero, err
	}
	defer func() { _ = tx.Rollback() }()
	current, permit, effects, bundle, owner, err := s.readPublicationPrivateInspectionTx(ctx, tx, capability)
	if err != nil {
		return zero, err
	}
	if observed.OriginalSealSHA256 != current.point.view.SourceSealSHA256 || !observed.EngineAbsent || !observed.ApplicationAbsent || !observed.IngressClosed || !observed.PrivateReadersAbsent || observed.ObservedAt.Location() != time.UTC || observed.ObservedAt.Before(current.point.view.PreparedAt) || observed.ObservedAt.After(time.Now().UTC()) || checkPublicationSourcePreservationAnchors(ctx, permit, effects, observed) != nil {
		return zero, ErrConflict
	}
	kind, module, schema, err := checkPublicationPrivateModuleObservation(ctx, current, bundle, owner, effects, observed)
	if err != nil {
		return zero, err
	}
	record := publicationSourcePreservationRecord{Format: "brine-publication-source-preservation-v1", Scope: current.cursor.Scope(), ResolverID: capability.attempt.resolverID, OriginalOutcomeSHA256: capability.attempt.originalOutcomeSHA, OriginalSealSHA256: observed.OriginalSealSHA256, WorkingKind: kind, WorkingEvidence: observed.WorkingEvidence.Bytes(), ModuleSHA256: module, SchemaSHA256: schema, ReaderNonce: observed.ReaderNonce, ReaderViewSHA256: observed.ReaderViewSHA256, ReaderImageID: observed.ReaderImageID, ReaderDisposedAt: observed.ReaderDisposedAt, ObservedAt: observed.ObservedAt}
	if len(record.WorkingEvidence) == 0 {
		record.WorkingEvidence = json.RawMessage("null")
	}
	raw, err := json.Marshal(record)
	if err != nil || len(raw) > 4<<20 {
		return zero, ErrInvalid
	}
	hash := publicationStorageSHA256(raw)
	if err = insertPublicationProofTx(ctx, tx, current.claim.PlanID, "source_preservation", publicationPreservationProof, raw); err != nil {
		return zero, err
	}
	next, err := current.cursor.PreserveSource(hash)
	if err != nil {
		return zero, ErrConflict
	}
	if err = appendPublicationCursorTx(ctx, tx, current.review.Plan(), current.cursor, next); err != nil {
		return zero, err
	}
	target := publicationTarget{runtime: permit.Runtime.Clone(), record: publicationTargetRecord{RuntimeSHA256: current.index.runtimeSHA}}
	stoppedCurrent := current
	stoppedCurrent.claim.NewEpochID = current.claim.OldEpochID
	if err = recordPublicationStoppedDesiredTx(ctx, tx, stoppedCurrent, target); err != nil {
		return zero, err
	}
	result, err := tx.ExecContext(ctx, "UPDATE data_fences SET state='released' WHERE id=? AND operation_id=? AND database_id=? AND incarnation_id=? AND state='held'", current.point.view.FenceID, current.point.view.FenceOwnerOpID, current.point.view.DatabaseID, current.point.view.IncarnationID)
	if err != nil {
		return zero, err
	}
	count, err := result.RowsAffected()
	if err != nil || count != 1 || !capability.active(s) || checkPublicationSourcePreservationAnchors(ctx, permit, effects, observed) != nil {
		return zero, errors.Join(ErrConflict, err)
	}
	receipt := publicationPreservedReceipt(current, capability.attempt.resolverID, hash)
	return receipt, tx.Commit()
}

func checkPublicationPrivateModuleObservation(ctx context.Context, current publicationAttemptState, bundle NativePreparedBundle, owner string, effects []PublicationEffect, observed PublicationSourcePreservationObservation) (string, string, string, error) {
	if len(effects) == 0 {
		if observed.WorkingRoot != nil || observed.WorkingEvidence.SHA256() != "" {
			return "", "", "", ErrConflict
		}
		return "no_native_effect", "", "", nil
	}
	if observed.WorkingRoot == nil {
		return "", "", "", ErrConflict
	}
	if bundle.manifest.MatchesTree(ctx, observed.WorkingRoot) {
		return "original_bundle", current.point.view.ArtifactWasmSHA256, current.point.view.SchemaSHA256, nil
	}
	evidence, err := nativeevidence.Decode(observed.WorkingEvidence.Bytes(), observed.WorkingEvidence.RequestedLimits())
	if err != nil || evidence.SHA256() != observed.WorkingEvidence.SHA256() || evidence.OwnerIdentity() != owner || evidence.PersistedConnectedClients() != 0 || !data.ValidID(observed.ReaderNonce) || !data.ValidCatalogHash(observed.ReaderViewSHA256) || !data.ValidCatalogHash(observed.ReaderImageID) || observed.ReaderDisposedAt.Location() != time.UTC || observed.ReaderDisposedAt.Before(current.point.view.PreparedAt) || observed.ReaderDisposedAt.After(observed.ObservedAt) {
		return "", "", "", ErrConflict
	}
	program := evidence.Program()
	if current.point.view.SourceKind == "initialized" && evidence.DatabaseIdentity() != current.point.view.NativeDatabase {
		return "", "", "", ErrConflict
	}
	candidate := current.review.Plan().Candidate()
	if program.Kind == "wasm" && program.SHA256 == candidate.WasmSHA256() && program.Wire == candidate.ProgramWireHash() {
		return "candidate", program.SHA256, candidate.Schema().SHA256(), nil
	}
	if current.point.view.SourceKind == "initialized" && program.Kind == "wasm" && program.SHA256 == current.point.view.ArtifactWasmSHA256 && program.Wire == current.point.view.ProgramWireHash {
		return "original_module", program.SHA256, current.point.view.SchemaSHA256, nil
	}
	return "", "", "", ErrConflict
}

func publicationPreservedReceipt(current publicationAttemptState, resolverID, hash string) datapublish.TaskReceipt {
	scope := current.cursor.Scope()
	return datapublish.TaskReceipt{Format: "brine-module-publication-task-v1", Kind: "module_publish_resolve", OperationID: resolverID, ApplyOperationID: current.claim.ApplyOpID, PlanID: scope.PlanID, PointID: scope.PointID, BindingID: scope.BindingID, OldEpochID: scope.OldEpochID, NewEpochID: scope.NewEpochID, TransitionState: "not_admitted", CompletionSHA256: hash, Outcome: "source_preserved"}
}

func publicationPreservedReceiptTx(ctx context.Context, tx *sql.Tx, current publicationAttemptState, resolverID string) (datapublish.TaskReceipt, error) {
	raw, hash, err := readPublicationProofTx(ctx, tx, current.claim.PlanID, "source_preservation", "source_preservation")
	var record publicationSourcePreservationRecord
	if err != nil || json.Unmarshal(raw, &record) != nil {
		return datapublish.TaskReceipt{}, errors.Join(&IntegrityError{}, err)
	}
	canonical, _ := json.Marshal(record)
	if !bytes.Equal(canonical, raw) || record.Format != "brine-publication-source-preservation-v1" || record.Scope != current.cursor.Scope() || record.OriginalSealSHA256 != current.point.view.SourceSealSHA256 || !data.ValidCatalogHash(record.OriginalOutcomeSHA256) {
		return datapublish.TaskReceipt{}, &IntegrityError{}
	}
	return publicationPreservedReceipt(current, resolverID, hash), nil
}
