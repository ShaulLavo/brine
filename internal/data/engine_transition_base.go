package data

import "encoding/json"

type EngineUnpublishedBase struct {
	Head       EngineHeadRef     `json:"head"`
	Allocation EngineEvidenceRef `json:"allocation"`
}

type EngineTransitionBase struct {
	kind        string
	committed   EngineHeadRef
	unpublished EngineUnpublishedBase
}

func CommittedEngineBase(head EngineHeadRef) (EngineTransitionBase, error) {
	if head.Validate() != nil {
		return EngineTransitionBase{}, ErrInvalid
	}
	return EngineTransitionBase{kind: "committed", committed: head}, nil
}

func UnpublishedEngineBase(base EngineUnpublishedBase) (EngineTransitionBase, error) {
	if base.Head.Validate() != nil ||
		!base.Allocation.validFor(EngineAllocationEvidence) || base.Allocation.BindingID != base.Head.BindingID || base.Allocation.EpochID != base.Head.EpochID {
		return EngineTransitionBase{}, ErrInvalid
	}
	return EngineTransitionBase{kind: "unpublished", unpublished: base}, nil
}

func (b EngineTransitionBase) Kind() string { return b.kind }
func (b EngineTransitionBase) Committed() (EngineHeadRef, bool) {
	return b.committed, b.kind == "committed"
}
func (b EngineTransitionBase) Unpublished() (EngineUnpublishedBase, bool) {
	return b.unpublished, b.kind == "unpublished"
}

func (b EngineTransitionBase) binding() ReplicaBindingID {
	if b.kind == "committed" {
		return b.committed.BindingID
	}
	return b.unpublished.Head.BindingID
}

func (b EngineTransitionBase) epoch() ReplicaEpochID {
	if b.kind == "committed" {
		return b.committed.EpochID
	}
	return b.unpublished.Head.EpochID
}

func (b EngineTransitionBase) Head() EngineHeadRef {
	if b.kind == "committed" {
		return b.committed
	}
	return b.unpublished.Head
}

func (b EngineTransitionBase) valid() bool {
	switch b.kind {
	case "committed":
		return b.committed.Validate() == nil
	case "unpublished":
		_, err := UnpublishedEngineBase(b.unpublished)
		return err == nil
	default:
		return false
	}
}

func (b EngineTransitionBase) MarshalJSON() ([]byte, error) {
	if !b.valid() {
		return nil, ErrInvalid
	}
	if b.kind == "committed" {
		return json.Marshal(map[string]any{"kind": b.kind, b.kind: b.committed})
	}
	return json.Marshal(map[string]any{"kind": b.kind, b.kind: b.unpublished})
}

func (b *EngineTransitionBase) UnmarshalJSON(raw []byte) error {
	fields, err := engineRecordFields(raw, []string{"kind"}, []string{"committed", "unpublished"})
	if err != nil || len(fields) != 2 {
		return ErrInvalid
	}
	var kind string
	if json.Unmarshal(fields["kind"], &kind) != nil || fields[kind] == nil {
		return ErrInvalid
	}
	decoded, err := decodeEngineBase(kind, fields[kind])
	if err != nil {
		return err
	}
	*b = decoded
	return nil
}

func decodeEngineBase(kind string, raw json.RawMessage) (EngineTransitionBase, error) {
	switch kind {
	case "committed":
		if engineRecord(raw, []string{"binding_id", "epoch_id", "version", "runtime_sha256"}, nil) != nil {
			return EngineTransitionBase{}, ErrInvalid
		}
		var head EngineHeadRef
		if json.Unmarshal(raw, &head) != nil {
			return EngineTransitionBase{}, ErrInvalid
		}
		return CommittedEngineBase(head)
	case "unpublished":
		fields, err := engineRecordFields(raw, []string{"head", "allocation"}, nil)
		if err != nil || engineRecord(fields["head"], []string{"binding_id", "epoch_id", "version", "runtime_sha256"}, nil) != nil || checkEngineEvidenceJSON(fields["allocation"]) != nil {
			return EngineTransitionBase{}, ErrInvalid
		}
		var base EngineUnpublishedBase
		if json.Unmarshal(raw, &base) != nil {
			return EngineTransitionBase{}, ErrInvalid
		}
		return UnpublishedEngineBase(base)
	default:
		return EngineTransitionBase{}, ErrInvalid
	}
}
