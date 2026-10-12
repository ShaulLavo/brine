package store

import (
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
