package datapublish

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"slices"
)

type cursorEvent struct {
	Kind        string       `json:"kind"`
	Step        Step         `json:"step"`
	Observation *Observation `json:"observation,omitempty"`
}
type cursorRecord struct {
	Version int           `json:"version"`
	Scope   ProofScope    `json:"scope"`
	State   CursorState   `json:"state"`
	Next    Step          `json:"next"`
	Events  []cursorEvent `json:"events"`
}

func cloneEvent(event cursorEvent) cursorEvent {
	if event.Observation == nil {
		return event
	}
	observation := *event.Observation
	observation.Requirements = slices.Clone(observation.Requirements)
	if observation.Requirements == nil {
		observation.Requirements = []RequirementProof{}
	}
	event.Observation = &observation
	return event
}
func (c *Cursor) appendEvent(event cursorEvent) {
	c.events = append(slices.Clone(c.events), cloneEvent(event))
}

func (c Cursor) MarshalJSON() ([]byte, error) {
	if !planID(c.plan.id) {
		return nil, ErrRecord
	}
	raw, err := json.Marshal(cursorRecord{Version: RecordVersion, Scope: c.scope, State: c.state, Next: c.step, Events: c.events})
	if err != nil || len(raw) > RecordLimit {
		return nil, ErrRecord
	}
	return raw, nil
}

// DecodeCursor requires the exact record hash from independently retained private
// state. Computing retainedSHA256 from raw cannot prevent a coherent rewind.
func DecodeCursor(raw []byte, plan Plan, retainedSHA256 string) (Cursor, error) {
	if len(raw) == 0 || len(raw) > RecordLimit || !digest(retainedSHA256) {
		return Cursor{}, ErrRecord
	}
	sum := sha256.Sum256(raw)
	if hex.EncodeToString(sum[:]) != retainedSHA256 {
		return Cursor{}, ErrRecord
	}
	if _, err := objectValues(raw, "version", "scope", "state", "next", "events"); err != nil {
		return Cursor{}, ErrRecord
	}
	var record cursorRecord
	if json.Unmarshal(raw, &record) != nil || record.Version != RecordVersion || record.Scope.PlanID != plan.id || len(record.Events) > 18 {
		return Cursor{}, ErrRecord
	}
	if err := validateCursorObjects(raw); err != nil {
		return Cursor{}, err
	}
	cursor, err := NewCursor(plan, record.Scope.ApplyOpID, record.Scope.ConsumerClaimSHA256, record.Scope.NewEpochID)
	if err != nil || cursor.scope != record.Scope {
		return Cursor{}, ErrRecord
	}
	for _, event := range record.Events {
		cursor, err = cursor.replay(event)
		if err != nil {
			return Cursor{}, ErrRecord
		}
	}
	if cursor.state != record.State || cursor.step != record.Next {
		return Cursor{}, ErrRecord
	}
	return cursor, nil
}

func (c Cursor) replay(event cursorEvent) (Cursor, error) {
	if event.Step != c.step {
		return c, ErrRecord
	}
	switch event.Kind {
	case "intent":
		if event.Observation != nil {
			return c, ErrRecord
		}
		return c.Intend(event.Step)
	case "unknown":
		if event.Observation != nil {
			return c, ErrRecord
		}
		return c.MarkUnknown()
	case "observation":
		if event.Observation == nil {
			return c, ErrRecord
		}
		return c.Observe(*event.Observation)
	case "source_preserved":
		o := event.Observation
		if o == nil || o.Scope != c.scope || o.Step != c.step || o.Outcome != ObservedSourcePreserved || o.NativeDatabase != "" || o.NativePublisher != "" || o.ProgramWireHash != "" || o.SchemaSHA256 != "" || len(o.Requirements) != 0 {
			return c, ErrRecord
		}
		return c.PreserveSource(o.ProofSHA256)
	default:
		return c, ErrRecord
	}
}

func validateCursorObjects(raw []byte) error {
	fields, err := objectValues(raw, "version", "scope", "state", "next", "events")
	if err != nil {
		return err
	}
	if err = validateProofScope(fields["scope"]); err != nil {
		return err
	}
	events, err := decodeRawArray(fields["events"])
	if err != nil {
		return err
	}
	for _, event := range events {
		if err = validateCursorEvent(event); err != nil {
			return err
		}
	}
	return nil
}

func decodeRawArray(raw json.RawMessage) ([]json.RawMessage, error) {
	var values []json.RawMessage
	if json.Unmarshal(raw, &values) != nil || values == nil {
		return nil, ErrRecord
	}
	return values, nil
}

func validateCursorEvent(raw json.RawMessage) error {
	var kind struct {
		Kind string `json:"kind"`
	}
	if json.Unmarshal(raw, &kind) != nil {
		return ErrRecord
	}
	fields := []string{"kind", "step"}
	if kind.Kind == "observation" || kind.Kind == "source_preserved" {
		fields = append(fields, "observation")
	}
	values, err := objectValues(raw, fields...)
	if err != nil {
		return err
	}
	if kind.Kind == "observation" || kind.Kind == "source_preserved" {
		return validateObservation(values["observation"])
	}
	return nil
}

func validateProofScope(raw json.RawMessage) error {
	_, err := objectValues(raw, "plan_id", "apply_op_id", "preparation_op_id", "consumer_claim_sha256", "binding_id", "point_id", "fence_id", "old_epoch_id", "new_epoch_id", "source_seal_sha256", "candidate_wasm_sha256", "candidate_program_wire_hash", "candidate_schema_sha256", "requirements_sha256")
	return err
}

func validateObservation(raw json.RawMessage) error {
	values, err := objectValues(raw, "scope", "step", "outcome", "proof_sha256", "native_database", "native_publisher", "program_wire_hash", "schema_sha256", "requirements")
	if err != nil {
		return err
	}
	if err = validateProofScope(values["scope"]); err != nil {
		return err
	}
	proofs, err := decodeRawArray(values["requirements"])
	if err != nil {
		return err
	}
	for _, proof := range proofs {
		if _, err = objectValues(proof, "requirement", "sha256"); err != nil {
			return err
		}
	}
	return nil
}
