package liverestore

import (
	"bytes"
	"strings"
	"testing"

	"github.com/ShaulLavo/brine/internal/data"
)

func attemptFixture(t *testing.T) (Plan, Event) {
	t.Helper()
	p, err := NewPlan(planFixture(t))
	if err != nil {
		t.Fatal(err)
	}
	s := p.Spec()
	attempt := Attempt{PlanID: p.ID(), OperationID: "restore-apply-operation", TransitionID: strings.Repeat("e", 32), ConsumerSHA256: strings.Repeat("d", 64),
		PreparedPointID: s.Safety.PointID, SelectedPointID: s.Selected.PointID, FenceID: s.Safety.FenceID, FenceOwnerOpID: s.Safety.FenceOwnerOpID,
		BindingID: s.Facts.Head.BindingID, SourceEpochID: s.Facts.Head.EpochID, IncomingEpochID: data.ReplicaEpochID(strings.Repeat("d", 32))}
	event, err := BeginAttempt(p, attempt)
	if err != nil {
		t.Fatal(err)
	}
	return p, event
}

func TestLiveRestoreTransitionIDDoesNotReuseTheLongerTaskID(t *testing.T) {
	p, event := attemptFixture(t)
	a := event.Attempt()
	a.OperationID = strings.Repeat("f", 44)
	if _, err := BeginAttempt(p, a); err != nil {
		t.Fatal("actual task ID shape refused", err)
	}
	a.TransitionID = a.OperationID
	if _, err := BeginAttempt(p, a); err == nil {
		t.Fatal("44-character task ID admitted as a32-character transition ID")
	}
	a.TransitionID = ""
	if _, err := BeginAttempt(p, a); err == nil {
		t.Fatal("missing retained transition identity admitted")
	}
}

func TestLiveRestoreCrashBoundariesKeepOriginalPendingAttempt(t *testing.T) {
	p, event := attemptFixture(t)
	original := event.Attempt()
	decoded, err := DecodeEvent(p, nil, event.Bytes())
	if err != nil || decoded.SHA256() != event.SHA256() {
		t.Fatal("claim did not round trip", err)
	}
	for _, pair := range []struct{ intent, done Stage }{
		{RestoreIntent, Verified}, {TransitionIntent, Transitioned}, {FinishIntent, Completed},
	} {
		previous := event
		intent, err := RecordIntent(p, event, pair.intent)
		if err != nil || !intent.Pending() {
			t.Fatal("intent was not retained", err)
		}
		if _, err = RecordIntent(p, intent, pair.intent); err == nil {
			t.Fatal("unknown effect was retried")
		}
		if _, err = RecordIntent(p, intent, AbandonIntent); err == nil {
			t.Fatal("unknown effect allowed abandoning current state")
		}
		retained, err := DecodeEvent(p, &previous, intent.Bytes())
		if err != nil || retained.Attempt() != original || retained.SHA256() != intent.SHA256() {
			t.Fatal("restart changed original consumer or intent", err)
		}
		event, err = Settle(p, retained, pair.done, strings.Repeat("e", 64))
		if err != nil || event.Pending() || event.Attempt() != original || event.Sequence() != previous.Sequence()+2 {
			t.Fatal("observed settlement changed original scope", err)
		}
		if _, err = DecodeEvent(p, &intent, event.Bytes()); err != nil {
			t.Fatal("settled record refused", err)
		}
	}
	if !event.Terminal() || event.Stage() != Completed {
		t.Fatal("completed attempt remained open")
	}
	if _, err = RecordIntent(p, event, RestoreIntent); err == nil {
		t.Fatal("completed restore rewound to source restoration")
	}
}

func TestLiveRestoreJournalRejectsSkippingAndScopeRewrites(t *testing.T) {
	p, claim := attemptFixture(t)
	if _, err := RecordIntent(p, claim, TransitionIntent); err == nil {
		t.Fatal("unverified source reached transition")
	}
	if _, err := Settle(p, claim, Completed, strings.Repeat("e", 64)); err == nil {
		t.Fatal("claim manufactured completion")
	}
	intent, err := RecordIntent(p, claim, RestoreIntent)
	if err != nil {
		t.Fatal(err)
	}
	for _, raw := range [][]byte{
		bytes.Replace(intent.Bytes(), []byte(`"fence_owner_op_id":"prepared-operation"`), []byte(`"fence_owner_op_id":"different-operation"`), 1),
		bytes.Replace(intent.Bytes(), []byte(`"selected_point_id":"`+p.spec.Selected.PointID+`"`), []byte(`"selected_point_id":"`+p.spec.Safety.PointID+`"`), 1),
		bytes.Replace(intent.Bytes(), []byte(`"sequence":2`), []byte(`"sequence":3`), 1),
		bytes.Replace(intent.Bytes(), []byte(`"stage":"restore_intent"`), []byte(`"stage":"completed"`), 1),
	} {
		if _, err = DecodeEvent(p, &claim, raw); err == nil {
			t.Fatal("changed scope or skipped sequence admitted")
		}
	}
}

func TestLiveRestoreAbandonmentIsUnavailableAfterMovement(t *testing.T) {
	p, event := attemptFixture(t)
	intent, err := RecordIntent(p, event, AbandonIntent)
	if err != nil {
		t.Fatal(err)
	}
	abandoned, err := Settle(p, intent, AbandonedCurrent, strings.Repeat("e", 64))
	if err != nil || !abandoned.Terminal() {
		t.Fatal("known current-state abandonment refused", err)
	}
	for _, pair := range []struct{ intent, done Stage }{
		{RestoreIntent, Verified}, {TransitionIntent, Transitioned},
	} {
		event, err = RecordIntent(p, event, pair.intent)
		if err != nil {
			t.Fatal(err)
		}
		event, err = Settle(p, event, pair.done, strings.Repeat("e", 64))
		if err != nil {
			t.Fatal(err)
		}
	}
	if _, err = RecordIntent(p, event, AbandonIntent); err == nil {
		t.Fatal("moved/activated state abandoned as unchanged current head")
	}
}
