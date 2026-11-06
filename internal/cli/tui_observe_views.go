package cli

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/ShaulLavo/brine/internal/ops"
	"github.com/ShaulLavo/brine/internal/ui"
)

func (m tuiModel) operationScreen() ui.Screen {
	o := m.observe
	s := ui.Screen{
		Title: "brine · " + o.Flags.target + " › " + o.Operation.App + " › " + o.Operation.ID,
		Help:  m.help(helpKey{"↑↓ scroll", 0}, helpKey{"r read", 0}, keyBack, helpKey{"l logs", 1}),
	}
	state := words(string(o.Operation.State))
	s.Context = state
	if !o.At.IsZero() {
		s.Context += " · polled " + o.At.In(m.loc).Format("15:04:05")
	}
	s.Notice, s.NoticeTone = m.observationNotice()
	s.Body = topWindow(m.operationLines(), o.Offset, m.frame().BodyRows(s))
	return s
}

func (m tuiModel) observationNotice() (string, ui.Tone) {
	o := m.observe
	switch o.Phase {
	case observeReconnecting:
		return fmt.Sprintf("Lost contact with %s (attempt %d of 3). Observation does not stop the host job.", o.Flags.target, o.Failures), ui.Warn
	case observePaused:
		if o.Failures >= 3 {
			return fmt.Sprintf("Observation paused after 3 failed reads. r resumes from event %d.", o.Cursor), ui.Warn
		}
		return errorNotice(o.Err), ui.Bad
	case observeUncertain:
		return "Launch outcome is unknown. Observation paused; r reads again. Do not relaunch.", ui.Warn
	}
	if o.Loading {
		return "Reading operation…", ui.Muted
	}
	return "", ui.Plain
}

func (m tuiModel) operationLines() []string {
	o := m.observe
	op := o.Operation
	state := words(string(op.State))
	if o.Phase != observeSettled && op.State.IsTerminal() {
		state += " · reading history"
	}
	lines := m.message(state + " · " + words(string(op.Kind)))
	if op.PlanID != "" {
		lines = append(lines, m.message("plan "+shortDigest(op.PlanID))...)
	}
	lines = append(lines, "started "+op.CreatedAt.In(m.loc).Format("15:04:05"))
	if o.Phase == observeSettled {
		lines = append(lines, "settled "+op.UpdatedAt.In(m.loc).Format("15:04:05"))
	}
	if o.Dropped != 0 {
		lines = append(lines, m.message(fmt.Sprintf("%d older events not retained; showing the latest %d.", o.Dropped, len(o.Events)))...)
	}
	if !o.FromStart {
		lines = append(lines, m.message("Earlier history was not read in this session.")...)
	}
	if len(o.Events) == 0 && !o.At.IsZero() {
		lines = append(lines, "No recorded events.")
	}
	for _, event := range o.Events {
		lines = append(lines, eventText(event, m.loc))
	}
	if o.Outcome != nil && o.Outcome.Error != "" {
		lines = append(lines, m.message("Outcome: "+string(o.Outcome.Error))...)
	}
	guidance := recoveryGuidance(o.Flags, op, o.Events, o.FromStart && o.Dropped == 0 && o.Phase == observeSettled)
	if guidance != "" {
		lines = append(lines, m.message(guidance)...)
	}
	return lines
}

func eventText(event ops.Event, loc *time.Location) string {
	text := event.Kind
	switch event.Kind {
	case "state":
		text += "  " + words(string(event.State))
	case "step":
		var payload ops.StepPayload
		if json.Unmarshal(event.Payload, &payload) == nil {
			text += "  " + payload.Step + "  " + payload.Outcome + "  " + payload.Code
		}
	case "failure":
		var payload ops.FailurePayload
		if json.Unmarshal(event.Payload, &payload) == nil {
			text += "  " + payload.Code
		}
	case "launch":
		var payload ops.LaunchPayload
		if json.Unmarshal(event.Payload, &payload) == nil {
			text += "  " + payload.Outcome
		}
	}
	return fmt.Sprintf("#%d  %s  %s", event.Sequence, event.CreatedAt.In(loc).Format("15:04:05"), strings.TrimSpace(text))
}
