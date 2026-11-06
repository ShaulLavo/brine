package cli

import (
	"encoding/json"
	"errors"
	"time"

	"github.com/ShaulLavo/brine/internal/ops"
	"github.com/ShaulLavo/brine/internal/result"
)

func tuiOperationGoldenStates() map[string]tuiModel {
	states := make(map[string]tuiModel)
	for _, state := range []ops.State{ops.Starting, ops.Succeeded, ops.Failed, ops.RolledBack, ops.RecoveryRequired, ops.LaunchUnknown} {
		m := operationGolden(state)
		states["operation-"+string(state)] = m
	}
	m := operationGolden(ops.Starting)
	m.observe.At, m.observe.Events, m.observe.Cursor = fixtureReadTime, nil, 0
	states["operation-empty"] = m
	m = operationGolden(ops.Starting)
	m.observe.At = time.Time{}
	m.observe.Loading = true
	m.observe.Cursor = 0
	m.observe.Events = nil
	states["operation-loading"] = m
	m = operationGolden(ops.Starting)
	m.observe.Phase, m.observe.Failures = observeReconnecting, 2
	states["operation-reconnecting"] = m
	m = operationGolden(ops.Starting)
	m.observe.Phase, m.observe.Failures = observePaused, 3
	states["operation-paused"] = m
	m = operationGolden(ops.Starting)
	m.observe.Phase, m.observe.Err = observePaused, result.New(result.AppNotFound, errors.New("hidden cause"))
	states["operation-read-failed"] = m
	m = operationGolden(ops.RecoveryRequired)
	m.observe.Dropped = 128
	m.observe.Cursor += 128
	for i := range m.observe.Events {
		m.observe.Events[i].Sequence += 128
	}
	states["operation-truncated"] = m
	return states
}

func operationGolden(state ops.State) tuiModel {
	m := fixtureModel(fixtureHost(), "fixture")
	m.stopRead()
	m.screen, m.app = screenOperation, "fixture-api"
	m.back = []tuiScreen{screenApps, screenApp}
	op := operationFixture(state, 0, 0).Operation
	events := []ops.Event{
		{Sequence: 1, Kind: "launch", Payload: json.RawMessage(`{"outcome":"intent"}`), CreatedAt: fixtureReadTime},
		{Sequence: 2, Kind: "state", State: ops.Starting, CreatedAt: fixtureReadTime},
		{Sequence: 3, Kind: "step", Payload: json.RawMessage(`{"step":"start_unit","outcome":"completed"}`), CreatedAt: fixtureReadTime},
	}
	phase := observePolling
	if state.IsTerminal() {
		phase = observeSettled
	}
	if state == ops.LaunchUnknown {
		phase = observeUncertain
	}
	m.observe = &tuiObservation{Flags: m.flags, Operation: op, Events: events, Cursor: 3, FromStart: true, Phase: phase, At: fixtureReadTime}
	return m
}
