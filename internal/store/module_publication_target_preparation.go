package store

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/datapublish"
	"github.com/ShaulLavo/brine/internal/nativecapture"
)

type publicationTargetIntent struct {
	Format             string                  `json:"format"`
	Scope              datapublish.ProofScope  `json:"scope"`
	TransitionID       string                  `json:"transition_id"`
	Binding            data.SpacetimeDBBinding `json:"binding"`
	VerificationSHA256 string                  `json:"verification_sha256"`
	DisposalSHA256     string                  `json:"disposal_sha256"`
	Runtime            data.EngineRuntime      `json:"runtime"`
	Checkpoint         json.RawMessage         `json:"checkpoint"`
	RecordedAt         time.Time               `json:"recorded_at"`
}

// PublicationTargetPreparation owns a fixed target location under the actual
// binding root. Its24 intent is retained before creating that location.
type PublicationTargetPreparation struct {
	state     *Store
	attempt   *PublicationAttempt
	lifetime  *data.EngineLock
	intent    publicationTargetIntent
	intentSHA string
}

type PublicationTargetFacts struct {
	Binding      data.SpacetimeDBBinding
	TransitionID string
	IntentSHA256 string
	Runtime      data.EngineRuntime
	Checkpoint   nativecapture.Checkpoint
}

func (PublicationTargetPreparation) MarshalJSON() ([]byte, error) { return nil, ErrInvalid }
func (PublicationTargetPreparation) String() string {
	return "[private publication target preparation]"
}
func (PublicationTargetPreparation) GoString() string {
	return "[private publication target preparation]"
}

func (p *PublicationTargetPreparation) active(s *Store) bool {
	return p != nil && p.state == s && p.attempt.active(s) && p.lifetime.HoldsLifetime(s.dir, p.attempt.binding) && p.intent.Binding.BindingID == p.attempt.binding
}

func (s *Store) BeginPublicationTargetPreparation(ctx context.Context, attempt *PublicationAttempt, lifetime *data.EngineLock) (*PublicationTargetPreparation, error) {
	if !attempt.active(s) || !lifetime.HoldsLifetime(s.dir, attempt.binding) || attempt.restored.unchanged(ctx) != nil {
		return nil, ErrConflict
	}
	tx, cancel, err := s.beginWrite(ctx)
	defer cancel()
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	current, err := s.readPublicationAttemptTx(ctx, tx, attempt)
	step, ready := current.cursor.NextStep()
	if err != nil || !ready || step != datapublish.FinalTransition || checkNativePointCurrent(ctx, tx, current.point.view) != nil || checkPublicationPolicy(ctx, tx, current.review.Plan().Facts()) != nil {
		return nil, errors.Join(ErrConflict, err)
	}
	verified, verificationSHA, err := readPublicationVerificationTx(ctx, tx, attempt, current)
	if err != nil {
		return nil, err
	}
	_, disposalSHA, err := readPublicationDisposalTx(ctx, tx, attempt, current)
	if err != nil {
		return nil, err
	}
	permit, err := readEnginePermit(ctx, tx, attempt.binding)
	if err != nil {
		return nil, err
	}
	runtime, err := publicationTargetRuntimeTx(ctx, tx, current, permit.Runtime)
	if err != nil {
		return nil, err
	}
	intent := publicationTargetIntent{Format: "brine-publication-target-intent-v1", Scope: current.cursor.Scope(), TransitionID: current.claim.RepositoryIntentID, Binding: permit.Reserved.Database, VerificationSHA256: verificationSHA, DisposalSHA256: disposalSHA, Runtime: runtime, Checkpoint: bytes.Clone(verified.Checkpoint), RecordedAt: time.Now().UTC()}
	previous, previousSHA, err := readPublicationTargetIntentTx(ctx, tx, attempt.planID)
	if err == nil {
		intent.RecordedAt = previous.RecordedAt
		raw, _ := json.Marshal(intent)
		if publicationStorageSHA256(raw) != previousSHA {
			return nil, ErrConflict
		}
	} else if !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	raw, err := json.Marshal(intent)
	if err != nil || len(raw) > 2<<20 {
		return nil, ErrInvalid
	}
	hash := publicationStorageSHA256(raw)
	if previousSHA == "" {
		if _, err = tx.ExecContext(ctx, "INSERT INTO data_module_publication_target_intents VALUES(?,?,?,?)", attempt.planID, intent.TransitionID, hash, raw); err != nil {
			return nil, err
		}
	}
	capability := &PublicationTargetPreparation{state: s, attempt: attempt, lifetime: lifetime, intent: intent, intentSHA: hash}
	if !capability.active(s) || attempt.restored.unchanged(ctx) != nil {
		return nil, ErrConflict
	}
	return capability, tx.Commit()
}

func publicationTargetRuntimeTx(ctx context.Context, tx *sql.Tx, current publicationAttemptState, runtime data.EngineRuntime) (data.EngineRuntime, error) {
	approval, err := readPublicationReviewApprovalTx(ctx, tx, current.review, current.index)
	if err != nil {
		return data.EngineRuntime{}, err
	}
	_, desired, err := loadPlan(ctx, tx, approval.TargetDeclarationID)
	if err != nil || desired.SpacetimeDB == nil || desired.SpacetimeDB.Module.Metadata == nil {
		return data.EngineRuntime{}, errors.Join(ErrConflict, err)
	}
	candidate := current.review.Plan().Candidate()
	runtime = runtime.Clone()
	runtime.EpochID, runtime.State = current.claim.NewEpochID, data.EngineInitialized
	runtime.Database, runtime.Publisher = data.NativeDatabaseIdentity(current.cursor.NativeDatabase()), data.NativePublisherIdentity(current.cursor.NativePublisher())
	runtime.Module = data.EngineModuleDefinition{Program: data.NativeProgramWireHash(candidate.ProgramWireHash()), Wasm: data.WasmSHA256(candidate.WasmSHA256()), Schema: data.EngineSchemaDefinition{Marker: desired.SpacetimeDB.Module.Marker, Canonicalizer: candidate.Schema().Version(), SHA256: candidate.Schema().SHA256()}}
	if current.review.Plan().Source().Kind() == datapublish.CreateSource {
		runtime.Names = []string{current.review.Plan().CreationName()}
	}
	if runtime.Validate() != nil {
		return data.EngineRuntime{}, ErrConflict
	}
	schema, _ := json.Marshal(runtime.Module.Schema)
	if err = saveEngineRegistry(ctx, tx, engineSchemaRecord, current.point.view.DatabaseID, runtime.Module.Schema.Marker, schema); err != nil {
		return data.EngineRuntime{}, err
	}
	module, _ := json.Marshal(runtime.Module)
	if err = saveEngineRegistry(ctx, tx, engineModuleRecord, current.point.view.DatabaseID, string(runtime.Module.Program), module); err != nil {
		return data.EngineRuntime{}, err
	}
	return runtime, nil
}

func readPublicationTargetIntentTx(ctx context.Context, tx *sql.Tx, planID string) (publicationTargetIntent, string, error) {
	var intent publicationTargetIntent
	var transition, hash string
	var raw []byte
	if err := tx.QueryRowContext(ctx, "SELECT transition_id,record_sha256,canonical FROM data_module_publication_target_intents WHERE plan_id=?", planID).Scan(&transition, &hash, &raw); err != nil {
		return intent, "", err
	}
	if len(raw) == 0 || len(raw) > 2<<20 || publicationStorageSHA256(raw) != hash || json.Unmarshal(raw, &intent) != nil {
		return intent, "", &IntegrityError{}
	}
	canonical, _ := json.Marshal(intent)
	checkpoint, err := nativecapture.DecodeCheckpoint(intent.Checkpoint, nativecapture.DefaultLimits())
	if !bytes.Equal(raw, canonical) || err != nil || intent.Format != "brine-publication-target-intent-v1" || intent.Scope.PlanID != planID || intent.TransitionID != transition || !data.ValidID(transition) || intent.Binding.Validate() != nil || intent.Runtime.Validate() != nil || intent.Runtime.EpochID != data.ReplicaEpochID(intent.Scope.NewEpochID) || checkpoint.Record().Scope.EpochID != intent.Scope.NewEpochID || intent.RecordedAt.Location() != time.UTC || intent.RecordedAt.IsZero() || intent.RecordedAt.After(time.Now().UTC()) {
		return intent, "", &IntegrityError{}
	}
	return intent, hash, nil
}

func (s *Store) ReadPublicationTargetFacts(ctx context.Context, capability *PublicationTargetPreparation) (PublicationTargetFacts, error) {
	if !capability.active(s) || capability.attempt.restored.unchanged(ctx) != nil {
		return PublicationTargetFacts{}, ErrConflict
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return PublicationTargetFacts{}, err
	}
	defer func() { _ = tx.Rollback() }()
	current, err := s.readPublicationAttemptTx(ctx, tx, capability.attempt)
	if err != nil {
		return PublicationTargetFacts{}, err
	}
	intent, hash, err := readPublicationTargetIntentTx(ctx, tx, capability.attempt.planID)
	if err != nil || hash != capability.intentSHA || intent.Scope != current.cursor.Scope() || intent.TransitionID != current.claim.RepositoryIntentID {
		return PublicationTargetFacts{}, errors.Join(ErrConflict, err)
	}
	checkpoint, err := nativecapture.DecodeCheckpoint(intent.Checkpoint, nativecapture.DefaultLimits())
	if err != nil {
		return PublicationTargetFacts{}, err
	}
	facts := PublicationTargetFacts{Binding: intent.Binding, TransitionID: intent.TransitionID, IntentSHA256: hash, Runtime: intent.Runtime.Clone(), Checkpoint: checkpoint}
	return facts, tx.Commit()
}
