package liverestore

// Loss counts committed log offsets only when both points have the same native
// history. It does not equate one transaction with one row or one user write.
type Loss struct {
	Kind                 string `json:"kind"`
	SelectedOffset       uint64 `json:"selected_offset"`
	CurrentOffset        uint64 `json:"current_offset"`
	CommittedOffsetsLost uint64 `json:"committed_offsets_lost"`
	UnknownReason        string `json:"unknown_reason"`
}

func deriveLoss(spec PlanSpec) (Loss, error) {
	s, f := spec.Selected, spec.Facts
	if s.EpochID != f.Head.EpochID || s.GenerationID != f.GenerationID {
		return Loss{Kind: "unknown", UnknownReason: "selected point belongs to another native history"}, nil
	}
	if s.CommittedOffset > spec.Safety.CommittedOffset {
		return Loss{}, ErrRecord
	}
	return Loss{Kind: "known", SelectedOffset: s.CommittedOffset, CurrentOffset: spec.Safety.CommittedOffset,
		CommittedOffsetsLost: spec.Safety.CommittedOffset - s.CommittedOffset}, nil
}
