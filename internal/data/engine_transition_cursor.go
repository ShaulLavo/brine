package data

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"math"
)

const EngineTransitionEventFormat = "brine-engine-transition-event-v1"

type EngineTransitionStage string

const (
	EngineTransitionPrepared          EngineTransitionStage = "prepared"
	EngineTransitionPreserveIntent    EngineTransitionStage = "preserve_intent"
	EngineTransitionOriginalPreserved EngineTransitionStage = "original_preserved"
	EngineTransitionPromoteIntent     EngineTransitionStage = "promote_intent"
	EngineTransitionTargetPromoted    EngineTransitionStage = "target_promoted"
	EngineTransitionActivationIntent  EngineTransitionStage = "activation_intent"
	EngineTransitionActivated         EngineTransitionStage = "activated"
	EngineTransitionFinishIntent      EngineTransitionStage = "finish_intent"
	EngineTransitionCompleted         EngineTransitionStage = "completed"
	EngineTransitionAbandonIntent     EngineTransitionStage = "abandon_intent"
	EngineTransitionAbandoned         EngineTransitionStage = "abandoned_current"
)

type EngineTransitionCursor struct {
	ID          string                `json:"id"`
	Sequence    uint64                `json:"sequence"`
	Stage       EngineTransitionStage `json:"stage"`
	InputSHA256 string                `json:"input_sha256"`
	EventSHA256 string                `json:"event_sha256"`
}

func (c EngineTransitionCursor) matches(t EngineTransition) bool {
	return t.hash != "" && c.ID == t.common.Scope.ID && c.Sequence > 0 && c.Sequence < math.MaxInt64 &&
		knownEngineTransitionStage(c.Stage) && c.InputSHA256 == t.hash && engineDigest(c.EventSHA256)
}

func knownEngineTransitionStage(stage EngineTransitionStage) bool {
	switch stage {
	case EngineTransitionPrepared, EngineTransitionPreserveIntent, EngineTransitionOriginalPreserved,
		EngineTransitionPromoteIntent, EngineTransitionTargetPromoted, EngineTransitionActivationIntent,
		EngineTransitionActivated, EngineTransitionFinishIntent, EngineTransitionCompleted,
		EngineTransitionAbandonIntent, EngineTransitionAbandoned:
		return true
	default:
		return false
	}
}

// EngineTransitionEvent records structural progress, never host-effect authority.
type EngineTransitionEvent struct {
	cursor   EngineTransitionCursor
	previous string
	encoded  []byte
}

func (e EngineTransitionEvent) Cursor() EngineTransitionCursor { return e.cursor }
func (e EngineTransitionEvent) PreviousSHA256() string         { return e.previous }
func (e EngineTransitionEvent) Bytes() []byte                  { return bytes.Clone(e.encoded) }

func PrepareEngineTransition(t EngineTransition) (EngineTransitionEvent, error) {
	if t.hash == "" {
		return EngineTransitionEvent{}, ErrInvalid
	}
	cursor := EngineTransitionCursor{ID: t.common.Scope.ID, Sequence: 1, Stage: EngineTransitionPrepared, InputSHA256: t.hash}
	return encodeEngineTransitionEvent(cursor, "", "prepared", t.hash)
}

func AdvanceEngineTransition(t EngineTransition, old EngineTransitionCursor, next EngineTransitionStage, observed EngineLayoutObservation) (EngineTransitionEvent, error) {
	if !old.matches(t) {
		return EngineTransitionEvent{}, ErrInvalid
	}
	wanted, allowed := engineStepLayout(old.Stage, next)
	if !allowed || ClassifyEngineTransitionLayout(t, observed) != wanted {
		return EngineTransitionEvent{}, ErrInvalid
	}
	return nextEngineTransitionEvent(t, old, next, "layout", observed)
}

func engineStepLayout(from, to EngineTransitionStage) (EngineLayoutState, bool) {
	switch from {
	case EngineTransitionPrepared:
		return EngineLayoutPrepared, to == EngineTransitionPreserveIntent || to == EngineTransitionAbandonIntent
	case EngineTransitionPreserveIntent:
		if to == EngineTransitionAbandonIntent {
			return EngineLayoutPrepared, true
		}
		return EngineLayoutPreserved, to == EngineTransitionOriginalPreserved
	case EngineTransitionOriginalPreserved:
		return EngineLayoutPreserved, to == EngineTransitionPromoteIntent
	case EngineTransitionPromoteIntent:
		return EngineLayoutPromoted, to == EngineTransitionTargetPromoted
	case EngineTransitionTargetPromoted:
		return EngineLayoutPromoted, to == EngineTransitionActivationIntent
	case EngineTransitionActivated:
		return EngineLayoutPromoted, to == EngineTransitionFinishIntent
	default:
		return EngineLayoutConflict, false
	}
}

func RecordEngineTransitionActivation(t EngineTransition, old EngineTransitionCursor, head EngineHeadRef) (EngineTransitionEvent, error) {
	if !old.matches(t) || old.Stage != EngineTransitionActivationIntent || head.Validate() != nil ||
		head.BindingID != t.common.Scope.BindingID || head.EpochID != t.common.NewEpochID || head.RuntimeSHA256 != t.common.TargetRuntimeSHA256 {
		return EngineTransitionEvent{}, ErrInvalid
	}
	version := uint64(1)
	if before, ok := t.common.Base.Committed(); ok {
		if before.Version >= math.MaxInt64 {
			return EngineTransitionEvent{}, ErrInvalid
		}
		version = before.Version + 1
	}
	if head.Version != version {
		return EngineTransitionEvent{}, ErrInvalid
	}
	return nextEngineTransitionEvent(t, old, EngineTransitionActivated, "activation", head)
}

func nextEngineTransitionEvent(t EngineTransition, old EngineTransitionCursor, next EngineTransitionStage, kind string, evidence any) (EngineTransitionEvent, error) {
	cursor := EngineTransitionCursor{ID: old.ID, Sequence: old.Sequence + 1, Stage: next, InputSHA256: t.hash}
	return encodeEngineTransitionEvent(cursor, old.EventSHA256, kind, evidence)
}

func encodeEngineTransitionEvent(cursor EngineTransitionCursor, previous, kind string, evidence any) (EngineTransitionEvent, error) {
	raw, err := json.Marshal(map[string]any{
		"format": EngineTransitionEventFormat, "id": cursor.ID, "sequence": cursor.Sequence, "stage": cursor.Stage,
		"input_sha256": cursor.InputSHA256, "previous_sha256": previous, "evidence_kind": kind, "evidence": evidence,
	})
	if err != nil || len(raw) > MaxEngineTransitionBytes {
		return EngineTransitionEvent{}, ErrInvalid
	}
	sum := sha256.Sum256(raw)
	cursor.EventSHA256 = hex.EncodeToString(sum[:])
	return EngineTransitionEvent{cursor: cursor, previous: previous, encoded: raw}, nil
}
