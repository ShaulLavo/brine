//go:build linux

package store

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/ShaulLavo/brine/internal/data"
)

func TestPublicationUnitAbsenceBelongsOnlyToUnpublishedSource(t *testing.T) {
	hash := strings.Repeat("a", 64)
	for _, tc := range []struct {
		name     string
		initial  bool
		observed string
		absent   bool
		want     bool
	}{
		{"unpublished absence", true, "", true, true},
		{"unpublished fabricated hash", true, hash, true, false},
		{"unpublished installed service", true, hash, false, false},
		{"unpublished unknown unit", true, "", false, false},
		{"initialized exact service", false, hash, false, true},
		{"initialized absence", false, "", true, false},
		{"initialized missing hash", false, "", false, false},
		{"initialized foreign hash", false, strings.Repeat("b", 64), false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := publicationTargetUnitMatches(tc.initial, hash, tc.observed, tc.absent); got != tc.want {
				t.Fatal("unit observation crossed publication source state")
			}
		})
	}
}

func TestPublicationTargetRetainsUnitAbsenceForActivationAndRecovery(t *testing.T) {
	hash := strings.Repeat("a", 64)
	target := publicationTarget{runtime: data.EngineRuntime{UnitSHA256: hash}}
	if got := publicationTargetUnitSHA256(target); got != hash {
		t.Fatal("installed target lost its expected service hash")
	}
	target.record.UnitAbsent = true
	if got := publicationTargetUnitSHA256(target); got != "" {
		t.Fatal("absent target fabricated an installed service hash")
	}
}

func TestPublicationActivationRequiresTheRecordedUnitObservation(t *testing.T) {
	f := nativeTransitionStateFixture(t, data.EngineUnpublished)
	at := time.Now().UTC()
	target := publicationTarget{runtime: f.runtime, record: publicationTargetRecord{UnitAbsent: true, MaterialSHA256: strings.Repeat("b", 64), ObservedAt: at}}
	observed := PublicationTransitionObservation{Layout: storeTransitionLayout(t, f.input, data.EngineLayoutPromoted), UnitAbsent: true, MaterialSHA256: target.record.MaterialSHA256, EngineAbsent: true, ApplicationAbsent: true, IngressClosed: true, PrivateReadersAbsent: true, ObservedAt: at}
	if err := checkPublicationTransitionObservation(target, f.input, observed); err != nil {
		t.Fatal("initial transition refused its recorded absence", err)
	}
	observed.UnitAbsent = false
	if err := checkPublicationTransitionObservation(target, f.input, observed); err == nil {
		t.Fatal("initial transition accepted an unknown unit observation")
	}
	observed.UnitAbsent, observed.UnitSHA256 = true, f.runtime.UnitSHA256
	if err := checkPublicationTransitionObservation(target, f.input, observed); err == nil {
		t.Fatal("initial transition accepted an installed unit in place of absence")
	}
}

func TestPublicationCompletionReadbackRetainsInitialUnitAbsence(t *testing.T) {
	at := time.Now().UTC()
	target := publicationTarget{hash: strings.Repeat("b", 64), runtime: data.EngineRuntime{UnitSHA256: strings.Repeat("a", 64)}, record: publicationTargetRecord{UnitAbsent: true, MaterialSHA256: strings.Repeat("c", 64), ObservedAt: at}}
	completed := publicationCompletionRecord{TargetSHA256: target.hash, UnitAbsent: true, MaterialSHA256: target.record.MaterialSHA256, ObservedAt: at}
	raw, err := json.Marshal(completed)
	if err != nil {
		t.Fatal(err)
	}
	var readback publicationCompletionRecord
	if err = json.Unmarshal(raw, &readback); err != nil {
		t.Fatal(err)
	}
	if err = checkPublicationCompletionTarget(target, readback); err != nil {
		t.Fatal("completed initial publication required an installed future service", err)
	}
	readback.UnitSHA256 = target.runtime.UnitSHA256
	if err = checkPublicationCompletionTarget(target, readback); err == nil {
		t.Fatal("completion substituted an installed service for retained absence")
	}
	readback.UnitSHA256, readback.UnitAbsent = "", false
	if err = checkPublicationCompletionTarget(target, readback); err == nil {
		t.Fatal("completion lost its explicit unit absence")
	}
	target.record.UnitAbsent = false
	readback.UnitSHA256 = target.runtime.UnitSHA256
	if err = checkPublicationCompletionTarget(target, readback); err != nil {
		t.Fatal("initialized completion refused exact service readback", err)
	}
	readback.UnitSHA256 = ""
	if err = checkPublicationCompletionTarget(target, readback); err == nil {
		t.Fatal("initialized completion accepted a missing service")
	}
}
