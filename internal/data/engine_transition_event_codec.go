package data

import "encoding/json"

func (e EngineTransitionEvent) MarshalJSON() ([]byte, error) {
	if len(e.encoded) == 0 || !engineDigest(e.cursor.EventSHA256) {
		return nil, ErrInvalid
	}
	return e.Bytes(), nil
}

func DecodeEngineTransitionEvent(t EngineTransition, previous *EngineTransitionCursor, raw []byte) (EngineTransitionEvent, error) {
	if len(raw) == 0 || len(raw) > MaxEngineTransitionBytes || t.hash == "" {
		return EngineTransitionEvent{}, ErrInvalid
	}
	fields, err := engineRecordFields(raw, []string{"format", "id", "sequence", "stage", "input_sha256", "previous_sha256", "evidence_kind", "evidence"}, nil)
	if err != nil {
		return EngineTransitionEvent{}, err
	}
	var header struct {
		Format   string                `json:"format"`
		ID       string                `json:"id"`
		Sequence uint64                `json:"sequence"`
		Stage    EngineTransitionStage `json:"stage"`
		Input    string                `json:"input_sha256"`
		Previous string                `json:"previous_sha256"`
		Kind     string                `json:"evidence_kind"`
	}
	if json.Unmarshal(raw, &header) != nil || header.Format != EngineTransitionEventFormat || header.ID != t.common.Scope.ID || header.Input != t.hash {
		return EngineTransitionEvent{}, ErrInvalid
	}
	decoded, err := decodeEngineEventEvidence(t, previous, header.Stage, header.Kind, fields["evidence"])
	if err != nil || decoded.cursor.Sequence != header.Sequence || decoded.previous != header.Previous {
		return EngineTransitionEvent{}, ErrInvalid
	}
	return decoded, nil
}

func decodeEngineEventEvidence(t EngineTransition, old *EngineTransitionCursor, stage EngineTransitionStage, kind string, raw []byte) (EngineTransitionEvent, error) {
	if old == nil {
		var input string
		if stage != EngineTransitionPrepared || kind != "prepared" || json.Unmarshal(raw, &input) != nil || input != t.hash {
			return EngineTransitionEvent{}, ErrInvalid
		}
		return PrepareEngineTransition(t)
	}
	switch kind {
	case "layout":
		var observation EngineLayoutObservation
		if json.Unmarshal(raw, &observation) != nil {
			return EngineTransitionEvent{}, ErrInvalid
		}
		return AdvanceEngineTransition(t, *old, stage, observation)
	case "activation":
		if stage != EngineTransitionActivated || engineRecord(raw, []string{"binding_id", "epoch_id", "version", "runtime_sha256"}, nil) != nil {
			return EngineTransitionEvent{}, ErrInvalid
		}
		var head EngineHeadRef
		if json.Unmarshal(raw, &head) != nil {
			return EngineTransitionEvent{}, ErrInvalid
		}
		return RecordEngineTransitionActivation(t, *old, head)
	default:
		return EngineTransitionEvent{}, ErrInvalid
	}
}
