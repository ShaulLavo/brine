package store

import (
	"bytes"
	"encoding/json"
	"github.com/ShaulLavo/brine/internal/datapublish"
	"strings"
	"testing"
	"time"
)

func TestPublicationEffectBindsDisposalAndRefusesAnotherStop(t *testing.T) {
	now := time.Now().UTC().Add(-time.Minute)
	effect := PublicationEffect{Nonce: strings.Repeat("1", 32), RecordedAt: now}
	started := PublicationEffectEvent{Nonce: effect.Nonce, Stage: "started", ContainerID: strings.Repeat("2", 64), PID: 41, StartTicks: 23, Cgroup: "/private/cgroup", RecordedAt: now.Add(time.Second)}
	if err := checkPublicationEffectEvent(effect, nil, started); err != nil {
		t.Fatal(err)
	}
	stopped := started
	stopped.Stage, stopped.RecordedAt = "stop_intent", now.Add(2*time.Second)
	if err := checkPublicationEffectEvent(effect, []PublicationEffectEvent{started}, stopped); err != nil {
		t.Fatal(err)
	}
	if checkPublicationEffectEvent(effect, []PublicationEffectEvent{started, stopped}, stopped) == nil {
		t.Fatal("retained stop intent permitted another stop")
	}
	disposed := started
	disposed.Stage, disposed.RecordedAt = "disposed", now.Add(3*time.Second)
	if err := checkPublicationEffectEvent(effect, []PublicationEffectEvent{started, stopped}, disposed); err != nil {
		t.Fatal(err)
	}
	disposed.StartTicks++
	if checkPublicationEffectEvent(effect, []PublicationEffectEvent{started, stopped}, disposed) == nil {
		t.Fatal("disposal of a reused PID admitted")
	}
	unknown := started
	unknown.Stage, unknown.RecordedAt = "unknown", now.Add(2*time.Second)
	if checkPublicationEffectEvent(effect, []PublicationEffectEvent{started, unknown}, stopped) == nil {
		t.Fatal("unknown effect permitted an automatic stop")
	}
}

func TestPublicationEffectRequiresObservedStartBeforeDisposal(t *testing.T) {
	now := time.Now().UTC().Add(-time.Minute)
	effect := PublicationEffect{Nonce: strings.Repeat("1", 32), RecordedAt: now}
	disposed := PublicationEffectEvent{Nonce: effect.Nonce, Stage: "disposed", ContainerID: strings.Repeat("2", 64), PID: 41, StartTicks: 23, Cgroup: "/private/cgroup", RecordedAt: now.Add(time.Second)}
	if checkPublicationEffectEvent(effect, nil, disposed) == nil {
		t.Fatal("disposal without an independently retained live process admitted")
	}
	unknown := disposed
	unknown.Stage, unknown.ContainerID, unknown.PID, unknown.StartTicks, unknown.Cgroup = "unknown", "", 0, 0, ""
	if err := checkPublicationEffectEvent(effect, nil, unknown); err != nil {
		t.Fatal("unknown launch could not be retained without fabricated process facts", err)
	}
}

func TestPublicationEffectFailureIsClosedAndPreservesAbsentCanonicalFields(t *testing.T) {
	now := time.Now().UTC().Add(-time.Minute)
	effect := PublicationEffect{Nonce: strings.Repeat("1", 32), Step: datapublish.PublishStage, RecordedAt: now}
	unknown := PublicationEffectEvent{Nonce: effect.Nonce, Stage: "unknown", RecordedAt: now.Add(time.Second)}
	raw, err := json.Marshal(unknown)
	if err != nil || bytes.Contains(raw, []byte("failure")) {
		t.Fatal("absent diagnostic changed retained canonical shape", err)
	}
	var decoded PublicationEffectEvent
	if json.Unmarshal(raw, &decoded) != nil {
		t.Fatal("retained event decode")
	}
	reproduced, err := json.Marshal(decoded)
	if err != nil || !bytes.Equal(raw, reproduced) {
		t.Fatal("retained canonical bytes changed", err)
	}
	unknown.Failure = &datapublish.StageFailure{Action: "create", Code: "guest_create_observe"}
	if err = checkPublicationEffectEvent(effect, nil, unknown); err != nil {
		t.Fatal("closed correlated failure refused", err)
	}
	encoded, err := json.Marshal(unknown)
	if err != nil || !bytes.Contains(encoded, []byte("guest_create_observe")) {
		t.Fatal("fixed phase did not survive storage encoding", err)
	}
	for _, failure := range []datapublish.StageFailure{{Action: "create", Code: "PRIVATE_RAW_SECRET"}, {Action: "arbitrary", Code: "guest_input"}, {Action: "preflight", Code: "guest_input"}} {
		unknown.Failure = &failure
		if checkPublicationEffectEvent(effect, nil, unknown) == nil {
			t.Fatal("unknown code/action or foreign step admitted")
		}
	}
	unknown.Failure = &datapublish.StageFailure{Action: "create", Code: "guest_input"}
	unknown.Stage = "started"
	if checkPublicationEffectEvent(effect, nil, unknown) == nil {
		t.Fatal("success/start event carried failure metadata")
	}
}
