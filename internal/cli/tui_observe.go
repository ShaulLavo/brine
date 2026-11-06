package cli

import (
	"fmt"
	"io"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/ShaulLavo/brine/internal/jobs"
	"github.com/ShaulLavo/brine/internal/ops"
	"github.com/ShaulLavo/brine/internal/result"
)

const tuiEventLimit = 1024

type observePhase uint8

const (
	observePolling observePhase = iota
	observeReconnecting
	observePaused
	observeUncertain
	observeSettled
)

// tuiObservation tracks only this client's reads, never host execution state.
type tuiObservation struct {
	Flags     operationFlags
	Operation ops.Operation
	Events    []ops.Event
	Outcome   *ops.TaskOutcome
	Cursor    uint64
	Dropped   int
	FromStart bool
	Phase     observePhase
	Failures  int
	Err       *result.Error
	At        time.Time
	Loading   bool
	Refreshed bool
	Offset    int
}

type pageMsg struct {
	token  uint64
	status jobs.Status
	err    error
}

type pollMsg struct{ token uint64 }

func tuiAfter(delay time.Duration, msg tea.Msg) tea.Cmd {
	return tea.Tick(delay, func(time.Time) tea.Msg { return msg })
}

func (m tuiModel) openOperation() (tea.Model, tea.Cmd) {
	status, found := m.appStatus()
	if !found || status.LastOperation == nil {
		return m, nil
	}
	op := *status.LastOperation
	if m.observe == nil || m.observe.Flags != m.flags || m.observe.Operation.ID != op.ID {
		m.observe = &tuiObservation{Flags: m.flags, Operation: op, FromStart: true}
	}
	m = m.push(screenOperation)
	return m.loadOperation()
}

func (m tuiModel) loadOperation() (tuiModel, tea.Cmd) {
	host := m.connect(m.observe.Flags)
	id, cursor := m.observe.Operation.ID, m.observe.Cursor
	m, ctx, token := m.beginRead()
	m.observe.Loading = true
	m.clampOperationOffset()
	return m, func() tea.Msg {
		status, err := host.Operation(ctx, id, cursor)
		return pageMsg{token: token, status: status, err: err}
	}
}

func (m tuiModel) operationPage(msg pageMsg) (tea.Model, tea.Cmd) {
	if m.screen != screenOperation || !m.current(msg.token) {
		return m, nil
	}
	defer m.clampOperationOffset()
	m.observe.Loading = false
	if msg.err != nil {
		return m.observationError(msg.err)
	}
	o := m.observe
	o.Operation, o.Outcome, o.Cursor = msg.status.Operation, msg.status.Outcome, msg.status.NextCursor
	o.appendEvents(msg.status.Events)
	o.At, o.Err, o.Failures = m.now(), nil, 0
	o.Phase = observePolling
	if len(msg.status.Events) == jobs.EventPageLimit {
		return m.loadOperation()
	}
	if o.Operation.State.IsTerminal() {
		o.Phase = observeSettled
		if !o.Refreshed {
			o.Refreshed = true
			return m.loadApps()
		}
		return m, nil
	}
	if o.Operation.State == ops.LaunchUnknown {
		o.Phase = observeUncertain
		return m, nil
	}
	return m, m.schedulePoll(time.Second)
}

func (m tuiModel) observationError(err error) (tea.Model, tea.Cmd) {
	defer m.clampOperationOffset()
	o := m.observe
	o.Err = result.Classify(err)
	o.Phase = observePaused
	if o.Err.Code() != result.TransportFailure {
		return m, nil
	}
	o.Failures++
	if o.Failures >= 3 {
		return m, nil
	}
	o.Phase = observeReconnecting
	return m, m.schedulePoll(min(15*time.Second, 2*time.Second<<(o.Failures-1)))
}

func (m tuiModel) schedulePoll(delay time.Duration) tea.Cmd {
	after := m.after
	if after == nil {
		after = tuiAfter
	}
	return after(delay, pollMsg{token: m.token})
}

func (m tuiModel) exitNotice() string {
	o := m.observe
	if o == nil || o.Operation.State.IsTerminal() {
		return ""
	}
	observe := o.Flags.command("status", "--operation", o.Operation.ID, "--after-cursor", fmt.Sprint(o.Cursor))
	if o.Operation.State == ops.LaunchUnknown {
		return recoveryGuidance(o.Flags, o.Operation, o.Events, false) + " Resume observation with: " + observe
	}
	return fmt.Sprintf("Operation %s is still running on %s; quitting did not stop it. Observe with: %s", o.Operation.ID, o.Flags.target, observe)
}

// Each validated host page has at most 128 events. The fixed-capacity window
// discards old payload references rather than retaining an oversized backing array.
func (o *tuiObservation) appendEvents(events []ops.Event) {
	if o.Events == nil {
		o.Events = make([]ops.Event, 0, tuiEventLimit)
	}
	drop := max(0, len(o.Events)+len(events)-tuiEventLimit)
	if drop > 0 {
		copy(o.Events, o.Events[drop:])
		o.Events = o.Events[:len(o.Events)-drop]
		o.Dropped += drop
		o.Offset = max(0, o.Offset-drop)
	}
	o.Events = append(o.Events, events...)
}

// The caller restores the terminal and cancels reads before writing this notice.
func writeTUIExitNotice(final tea.Model, output io.Writer) error {
	m, ok := final.(tuiModel)
	if !ok {
		return nil
	}
	notice := m.exitNotice()
	if notice == "" {
		return nil
	}
	_, err := fmt.Fprintln(output, notice)
	return err
}
