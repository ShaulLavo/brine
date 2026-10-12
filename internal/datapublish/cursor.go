package datapublish

import (
	"slices"

	"github.com/ShaulLavo/brine/internal/spacetimedb"
)

type Step string

const (
	RestoreStage    Step = "restore_stage"
	CheckPreflight  Step = "check_preflight"
	PublishStage    Step = "publish_stage"
	VerifyStage     Step = "verify_stage"
	DisposeStage    Step = "dispose_stage"
	FinalTransition Step = "final_transition"
)

type CursorState string

const (
	Ready           CursorState = "ready"
	IntentRecorded  CursorState = "intent_recorded"
	InspectRequired CursorState = "inspect_required"
	Finished        CursorState = "finished"
	AttemptFailed   CursorState = "attempt_failed"
)

type ObservedOutcome string

const (
	ObservedCompleted    ObservedOutcome = "completed"
	ObservedNotPerformed ObservedOutcome = "not_performed"
)

type ProofScope struct {
	PlanID                   string `json:"plan_id"`
	ApplyOpID                string `json:"apply_op_id"`
	PreparationOpID          string `json:"preparation_op_id"`
	ConsumerClaimSHA256      string `json:"consumer_claim_sha256"`
	BindingID                string `json:"binding_id"`
	PointID                  string `json:"point_id"`
	FenceID                  string `json:"fence_id"`
	OldEpochID               string `json:"old_epoch_id"`
	NewEpochID               string `json:"new_epoch_id"`
	SourceSealSHA256         string `json:"source_seal_sha256"`
	CandidateWasmSHA256      string `json:"candidate_wasm_sha256"`
	CandidateProgramWireHash string `json:"candidate_program_wire_hash"`
	CandidateSchemaSHA256    string `json:"candidate_schema_sha256"`
	RequirementsSHA256       string `json:"requirements_sha256"`
}

type RequirementProof struct {
	Requirement spacetimedb.SchemaRequirement `json:"requirement"`
	SHA256      string                        `json:"sha256"`
}

// Observation references later protected evidence. Self-reported hashes cannot
// establish a ledger claim, state preservation, or transition authority.
type Observation struct {
	Scope           ProofScope         `json:"scope"`
	Step            Step               `json:"step"`
	Outcome         ObservedOutcome    `json:"outcome"`
	ProofSHA256     string             `json:"proof_sha256"`
	NativeDatabase  string             `json:"native_database"`
	NativePublisher string             `json:"native_publisher"`
	ProgramWireHash string             `json:"program_wire_hash"`
	SchemaSHA256    string             `json:"schema_sha256"`
	Requirements    []RequirementProof `json:"requirements"`
}

// Cursor sequences one retained attempt. The journal must enforce one consumer
// per point/plan and authenticate every proof before performing any effects.
type Cursor struct {
	plan            Plan
	scope           ProofScope
	state           CursorState
	step            Step
	nativeDatabase  string
	nativePublisher string
	events          []cursorEvent
}

func NewCursor(plan Plan, applyOpID, consumerClaimSHA256, newEpochID string) (Cursor, error) {
	if !planID(plan.id) || !operationReference.MatchString(applyOpID) || !digest(consumerClaimSHA256) || !identity(newEpochID) || newEpochID == plan.facts.EpochID || applyOpID == plan.source.PreparationOpID() {
		return Cursor{}, ErrRecord
	}
	scope := ProofScope{PlanID: plan.id, ApplyOpID: applyOpID, PreparationOpID: plan.source.PreparationOpID(), ConsumerClaimSHA256: consumerClaimSHA256,
		BindingID: plan.facts.BindingID, PointID: plan.source.PointID(), FenceID: plan.source.FenceID(), OldEpochID: plan.facts.EpochID, NewEpochID: newEpochID, SourceSealSHA256: plan.source.SealSHA256(),
		CandidateWasmSHA256: plan.candidate.wasm, CandidateProgramWireHash: plan.candidate.program, CandidateSchemaSHA256: plan.candidate.schema.SHA256(), RequirementsSHA256: plan.requirementsSHA}
	cursor := Cursor{plan: plan, scope: scope, state: Ready, step: RestoreStage, events: []cursorEvent{}}
	if plan.source.kind == CapturedSource {
		cursor.nativeDatabase = plan.source.point.NativeDatabase
		cursor.nativePublisher = plan.source.point.NativePublisher
	}
	return cursor, nil
}

func (c Cursor) Scope() ProofScope      { return c.scope }
func (c Cursor) State() CursorState     { return c.state }
func (c Cursor) NativeDatabase() string { return c.nativeDatabase }
func (c Cursor) NextStep() (Step, bool) {
	if c.state != Ready {
		return "", false
	}
	return c.step, true
}

func (c Cursor) Intend(step Step) (Cursor, error) {
	if c.state != Ready || c.step != step {
		return c, ErrInspection
	}
	c.state = IntentRecorded
	c.appendEvent(cursorEvent{Kind: "intent", Step: step})
	return c, nil
}

func (c Cursor) MarkUnknown() (Cursor, error) {
	if c.state != IntentRecorded {
		return c, ErrInspection
	}
	c.state = InspectRequired
	c.appendEvent(cursorEvent{Kind: "unknown", Step: c.step})
	return c, nil
}

func (c Cursor) Observe(observation Observation) (Cursor, error) {
	if c.state != IntentRecorded && c.state != InspectRequired || observation.Scope != c.scope || observation.Step != c.step || !digest(observation.ProofSHA256) {
		return c, ErrInspection
	}
	if observation.Outcome == ObservedNotPerformed {
		if c.step == PublishStage && c.checkUnperformedPublication(observation) != nil {
			return c, ErrInspection
		}
		c.state = AttemptFailed
		c.appendEvent(cursorEvent{Kind: "observation", Step: c.step, Observation: &observation})
		return c, nil
	}
	if observation.Outcome != ObservedCompleted || c.checkCompletion(observation) != nil {
		return c, ErrInspection
	}
	if c.step == PublishStage {
		c.nativeDatabase = observation.NativeDatabase
		c.nativePublisher = observation.NativePublisher
	}
	c.appendEvent(cursorEvent{Kind: "observation", Step: c.step, Observation: &observation})
	c.step = successor(c.step)
	c.state = Ready
	if c.step == "" {
		c.state = Finished
	}
	return c, nil
}

func (c Cursor) checkUnperformedPublication(observation Observation) error {
	if c.plan.source.kind == CreateSource {
		if observation.NativeDatabase != "" || observation.NativePublisher != "" || observation.ProgramWireHash != "" || observation.SchemaSHA256 != "" {
			return ErrInspection
		}
		return nil
	}
	point := c.plan.source.point
	if observation.NativeDatabase != point.NativeDatabase || observation.NativePublisher != point.NativePublisher || observation.ProgramWireHash != point.ProgramWireHash || observation.SchemaSHA256 != c.plan.source.schema.SHA256() {
		return ErrInspection
	}
	return nil
}

func (c Cursor) checkCompletion(observation Observation) error {
	if c.step == RestoreStage || c.step == CheckPreflight {
		if c.plan.source.kind == CreateSource && (observation.NativeDatabase != "" || observation.NativePublisher != "" || observation.ProgramWireHash != "" || observation.SchemaSHA256 != "") {
			return ErrInspection
		}
		if c.plan.source.kind == CapturedSource && (observation.NativeDatabase != c.plan.source.point.NativeDatabase || observation.NativePublisher != c.plan.source.point.NativePublisher || observation.ProgramWireHash != c.plan.source.point.ProgramWireHash || observation.SchemaSHA256 != c.plan.source.schema.SHA256()) {
			return ErrInspection
		}
	}
	if c.step == CheckPreflight && c.plan.source.kind == CapturedSource && observation.ProofSHA256 != c.plan.preflight.ResponseSHA256 {
		return ErrInspection
	}
	if c.step == PublishStage || c.step == VerifyStage || c.step == DisposeStage || c.step == FinalTransition {
		if !digest(observation.NativeDatabase) || !digest(observation.NativePublisher) || observation.ProgramWireHash != c.plan.candidate.program || observation.SchemaSHA256 != c.plan.candidate.schema.SHA256() {
			return ErrInspection
		}
		if c.nativeDatabase != "" && (c.nativeDatabase != observation.NativeDatabase || c.nativePublisher != observation.NativePublisher) {
			return ErrInspection
		}
	}
	if c.step == VerifyStage {
		return exactRequirementProofs(c.plan.requirements, observation.Requirements)
	}
	if len(observation.Requirements) != 0 {
		return ErrInspection
	}
	return nil
}

func exactRequirementProofs(required []spacetimedb.SchemaRequirement, proofs []RequirementProof) error {
	if len(proofs) != len(required) {
		return ErrInspection
	}
	seen := map[spacetimedb.SchemaRequirement]bool{}
	for _, proof := range proofs {
		if !slices.Contains(required, proof.Requirement) || seen[proof.Requirement] || !digest(proof.SHA256) {
			return ErrInspection
		}
		seen[proof.Requirement] = true
	}
	return nil
}

func successor(step Step) Step {
	switch step {
	case RestoreStage:
		return CheckPreflight
	case CheckPreflight:
		return PublishStage
	case PublishStage:
		return VerifyStage
	case VerifyStage:
		return DisposeStage
	case DisposeStage:
		return FinalTransition
	default:
		return ""
	}
}
