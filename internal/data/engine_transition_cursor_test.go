package data

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func advanceTransitionFixture(t *testing.T, transition EngineTransition, old EngineTransitionCursor, next EngineTransitionStage, layout EngineLayoutState) EngineTransitionEvent {
	t.Helper()
	event, err := AdvanceEngineTransition(transition, old, next, transitionLayout(t, transition, layout))
	if err != nil {
		t.Fatal(next, err)
	}
	decoded, err := DecodeEngineTransitionEvent(transition, &old, event.Bytes())
	if err != nil || decoded.Cursor() != event.Cursor() || !bytes.Equal(decoded.Bytes(), event.Bytes()) {
		t.Fatal("event round trip changed its CAS cursor", next, err)
	}
	return event
}

func activationIntentFixture(t *testing.T, transition EngineTransition) EngineTransitionCursor {
	t.Helper()
	event, err := PrepareEngineTransition(transition)
	if err != nil {
		t.Fatal(err)
	}
	event = advanceTransitionFixture(t, transition, event.Cursor(), EngineTransitionPreserveIntent, EngineLayoutPrepared)
	event = advanceTransitionFixture(t, transition, event.Cursor(), EngineTransitionOriginalPreserved, EngineLayoutPreserved)
	event = advanceTransitionFixture(t, transition, event.Cursor(), EngineTransitionPromoteIntent, EngineLayoutPreserved)
	event = advanceTransitionFixture(t, transition, event.Cursor(), EngineTransitionTargetPromoted, EngineLayoutPromoted)
	event = advanceTransitionFixture(t, transition, event.Cursor(), EngineTransitionActivationIntent, EngineLayoutPromoted)
	return event.Cursor()
}

func TestCursorRecordsIntentsBeforeObservedNamespaceReceipts(t *testing.T) {
	transition := transitionFixture(t)
	initial, err := PrepareEngineTransition(transition)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := DecodeEngineTransitionEvent(transition, nil, initial.Bytes())
	if err != nil || decoded.Cursor() != initial.Cursor() {
		t.Fatal(err)
	}
	if _, err = AdvanceEngineTransition(transition, initial.Cursor(), EngineTransitionOriginalPreserved, transitionLayout(t, transition, EngineLayoutPreserved)); err == nil {
		t.Fatal("observed absence replaced durable preserve intent")
	}
	intent := advanceTransitionFixture(t, transition, initial.Cursor(), EngineTransitionPreserveIntent, EngineLayoutPrepared)
	if _, err = AdvanceEngineTransition(transition, intent.Cursor(), EngineTransitionOriginalPreserved, transitionLayout(t, transition, EngineLayoutPrepared)); err == nil {
		t.Fatal("unperformed movement received a success record")
	}
	if _, err = AdvanceEngineTransition(transition, intent.Cursor(), EngineTransitionAbandonIntent, transitionLayout(t, transition, EngineLayoutPreserved)); err == nil {
		t.Fatal("moved original abandoned as pristine")
	}
	advanceTransitionFixture(t, transition, intent.Cursor(), EngineTransitionAbandonIntent, EngineLayoutPrepared)
	bytesBefore := intent.Bytes()
	bytesBefore[0] = 'x'
	if intent.Bytes()[0] != '{' {
		t.Fatal("caller mutated event bytes")
	}
}

func TestActivationReceiptBindsOneSuccessorHeadAndRejectsFabricatedTerminal(t *testing.T) {
	transition := transitionFixture(t)
	old := activationIntentFixture(t, transition)
	c := transition.Common()
	head := EngineHeadRef{BindingID: c.Scope.BindingID, EpochID: c.NewEpochID, Version: 3, RuntimeSHA256: c.TargetRuntimeSHA256}
	event, err := RecordEngineTransitionActivation(transition, old, head)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := DecodeEngineTransitionEvent(transition, &old, event.Bytes())
	if err != nil || decoded.Cursor() != event.Cursor() {
		t.Fatal(err)
	}
	for _, mutate := range []func(*EngineHeadRef){
		func(h *EngineHeadRef) { h.Version-- }, func(h *EngineHeadRef) { h.EpochID = c.Base.epoch() },
		func(h *EngineHeadRef) { h.BindingID = ReplicaBindingID(strings.Repeat("1", 32)) },
		func(h *EngineHeadRef) { h.RuntimeSHA256 = strings.Repeat("9", 64) },
	} {
		changed := head
		mutate(&changed)
		if _, err := RecordEngineTransitionActivation(transition, old, changed); err == nil {
			t.Fatal("foreign or old activation head accepted")
		}
	}
	for _, stage := range []EngineTransitionStage{EngineTransitionActivated, EngineTransitionCompleted, EngineTransitionAbandoned} {
		if _, err := AdvanceEngineTransition(transition, old, stage, transitionLayout(t, transition, EngineLayoutPromoted)); err == nil {
			t.Fatal("generic advancement manufactured a terminal/runtime commit", stage)
		}
	}
	finish := advanceTransitionFixture(t, transition, event.Cursor(), EngineTransitionFinishIntent, EngineLayoutPromoted)
	proof := transitionEvidence(c.Scope, c.NewEpochID, EngineCompletionEvidence, 11)
	completed, err := RecordEngineTransitionCompletion(transition, finish.Cursor(), proof)
	if err != nil {
		t.Fatal(err)
	}
	finishCursor := finish.Cursor()
	if decoded, err = DecodeEngineTransitionEvent(transition, &finishCursor, completed.Bytes()); err != nil || decoded.Cursor() != completed.Cursor() {
		t.Fatal("completion reference changed through contextual decoding", err)
	}
	wrong := proof
	wrong.EpochID = c.Base.epoch()
	if _, err = RecordEngineTransitionCompletion(transition, finish.Cursor(), wrong); err == nil {
		t.Fatal("old epoch supplied target completion")
	}
	if _, err := RecordEngineTransitionActivation(transition, event.Cursor(), head); err == nil {
		t.Fatal("activation replayed after receipt")
	}
}

func TestAbandonmentUsesOriginalEpochAndCannotResumeCursor(t *testing.T) {
	transition := transitionFixture(t)
	prepared, err := PrepareEngineTransition(transition)
	if err != nil {
		t.Fatal(err)
	}
	abandon := advanceTransitionFixture(t, transition, prepared.Cursor(), EngineTransitionAbandonIntent, EngineLayoutPrepared)
	c := transition.Common()
	proof := transitionEvidence(c.Scope, c.Base.epoch(), EngineAbandonEvidence, 12)
	terminal, err := RecordEngineTransitionAbandonment(transition, abandon.Cursor(), proof)
	if err != nil {
		t.Fatal(err)
	}
	old := abandon.Cursor()
	decoded, err := DecodeEngineTransitionEvent(transition, &old, terminal.Bytes())
	if err != nil || decoded.Cursor() != terminal.Cursor() {
		t.Fatal(err)
	}
	wrong := proof
	wrong.EpochID = c.NewEpochID
	if _, err = RecordEngineTransitionAbandonment(transition, abandon.Cursor(), wrong); err == nil {
		t.Fatal("target epoch replaced original abandonment proof")
	}
	if _, err = AdvanceEngineTransition(transition, terminal.Cursor(), EngineTransitionPreserveIntent, transitionLayout(t, transition, EngineLayoutPrepared)); err == nil {
		t.Fatal("consumed abandoned attempt retried")
	}
}

func TestEventCodecRejectsWrongPreviousInputAndSequence(t *testing.T) {
	transition := transitionFixture(t)
	prepared, err := PrepareEngineTransition(transition)
	if err != nil {
		t.Fatal(err)
	}
	old := prepared.Cursor()
	event := advanceTransitionFixture(t, transition, old, EngineTransitionPreserveIntent, EngineLayoutPrepared)
	var fields map[string]json.RawMessage
	if json.Unmarshal(event.Bytes(), &fields) != nil {
		t.Fatal("fixture parse")
	}
	for _, field := range []string{"previous_sha256", "input_sha256", "sequence", "evidence", "stage"} {
		changed := make(map[string]json.RawMessage, len(fields))
		for name, value := range fields {
			changed[name] = value
		}
		changed[field] = json.RawMessage(`null`)
		raw, err := json.Marshal(changed)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = DecodeEngineTransitionEvent(transition, &old, raw); err == nil {
			t.Fatal("null event component accepted", field)
		}
	}
	other := old
	other.EventSHA256 = strings.Repeat("9", 64)
	if _, err = DecodeEngineTransitionEvent(transition, &other, event.Bytes()); err == nil {
		t.Fatal("event detached from recorded previous cursor")
	}
	if _, err = DecodeEngineTransitionEvent(transition, nil, event.Bytes()); err == nil {
		t.Fatal("movement intent recreated as initial record")
	}
}
