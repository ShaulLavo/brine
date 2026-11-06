package cli

import (
	"context"
	"fmt"

	tea "charm.land/bubbletea/v2"
	"github.com/ShaulLavo/brine/internal/apps"
	"github.com/ShaulLavo/brine/internal/jobs"
	"github.com/ShaulLavo/brine/internal/ops"
	"github.com/ShaulLavo/brine/internal/plan"
	"github.com/ShaulLavo/brine/internal/result"
)

type tuiAction string

const (
	actionRestart  tuiAction = "restart"
	actionStop     tuiAction = "stop"
	actionStart    tuiAction = "start"
	actionRollback tuiAction = "rollback"
)

type tuiReview struct {
	Action      tuiAction
	App, PlanID string
	Kind        plan.Kind
	Config      *apps.ConfigPlan
	Rollback    *apps.RollbackPlan
	Key         string
}

func (r tuiReview) applyable() bool {
	if !jobs.ValidPlanID(r.PlanID) || r.Kind != plan.Create && r.Kind != plan.Update {
		return false
	}
	if r.Config != nil {
		return len(r.Config.Conflicts) == 0
	}
	return r.Rollback != nil && len(r.Rollback.Conflicts) == 0
}

// Issued identity belongs to the session, not to a view or its cancelable read.
// Only Update changes its acceptance state; commands capture immutable values.
type issuedApply struct {
	RequestID uint64
	Flags     operationFlags
	Review    tuiReview
	Accepted  *jobs.Accepted
	InFlight  bool
	Err       *result.Error
}

type reviewMsg struct {
	token  uint64
	review tuiReview
	err    error
}

type acceptMsg struct {
	requestID uint64
	accepted  jobs.Accepted
	err       error
}

func (h connectedHost) Plan(ctx context.Context, action tuiAction, app string) (tuiReview, error) {
	r := tuiReview{Action: action, App: app}
	if action == actionRollback {
		p, err := planRollback(ctx, h.deps, h.flags, app, "")
		r.PlanID, r.Kind, r.Rollback = p.PlanID, p.Kind, &p
		return r, err
	}
	var change plan.ChangeKind
	switch action {
	case actionRestart:
		change = plan.RestartApp
	case actionStop:
		change = plan.StopApp
	case actionStart:
		change = plan.StartApp
	default:
		return r, result.New(result.InvalidUsage, nil)
	}
	p, err := planLifecycle(ctx, h.deps, h.flags, app, change)
	r.PlanID, r.Kind, r.Config = p.PlanID, p.Kind, &p
	return r, err
}

func (h connectedHost) Apply(ctx context.Context, planID, key string) (jobs.Accepted, error) {
	return acceptPlan(ctx, h.deps, h.flags, planID, key)
}

func (m tuiModel) actions() []tuiAction {
	actions := []tuiAction{actionRestart, actionStop, actionStart}
	if status, found := m.appStatus(); found && status.Previous != nil {
		actions = append(actions, actionRollback)
	}
	return actions
}

func (m tuiModel) openActions() (tea.Model, tea.Cmd) {
	if m.issued != nil {
		return m.openIssued()
	}
	if _, found := m.appStatus(); !found {
		return m, nil
	}
	m.stopRead()
	m.actionIndex = 0
	return m.push(screenActions), nil
}

func (m tuiModel) planAction() (tea.Model, tea.Cmd) {
	if m.issued != nil {
		return m, nil
	}
	action := m.actions()[clampIndex(m.actionIndex, len(m.actions()))]
	m.review = loadable[tuiReview]{Value: tuiReview{Action: action, App: m.app}}
	m.reviewFlags = m.flags
	m.reviewOffset = 0
	m = m.push(screenReview)
	return m.loadReview()
}

func (m tuiModel) loadReview() (tuiModel, tea.Cmd) {
	if m.issued != nil {
		return m, nil
	}
	host := m.connect(m.reviewFlags)
	action, app := m.review.Value.Action, m.review.Value.App
	m, ctx, token := m.beginRead()
	m.review.start()
	m.confirm = false
	return m, func() tea.Msg {
		review, err := host.Plan(ctx, action, app)
		return reviewMsg{token: token, review: review, err: err}
	}
}

func (m tuiModel) canApply() bool {
	if m.screen != screenReview || m.review.State != loadReady || !m.review.Value.applyable() {
		return false
	}
	return m.issued == nil || !m.issued.InFlight && m.issued.Accepted == nil && m.reviewFlags == m.issued.Flags && m.review.Value.PlanID == m.issued.Review.PlanID
}

func (m tuiModel) confirmApply() (tea.Model, tea.Cmd) {
	if m.canApply() {
		m.confirm = true
	}
	return m, nil
}

func (m tuiModel) issueApply() (tea.Model, tea.Cmd) {
	if !m.canApply() || !m.frame().NoticeVisible(m.reviewScreen()) {
		return m, nil
	}
	m.confirm = false
	if m.issued == nil {
		key, err := m.newKey()
		if err != nil {
			m.review.Err = result.Classify(err)
			return m, nil
		}
		if !jobs.ValidID(key) {
			m.review.Err = result.New(result.InternalError, nil)
			return m, nil
		}
		m.review.Value.Key = key
		m.nextIssued++
		m.issued = &issuedApply{RequestID: m.nextIssued, Flags: m.reviewFlags, Review: m.review.Value}
	}
	m.stopRead()
	issued := m.issued
	issued.InFlight, issued.Err = true, nil
	host := m.connect(issued.Flags)
	id, planID, key, ctx := issued.RequestID, issued.Review.PlanID, issued.Review.Key, m.ctx
	return m, func() tea.Msg {
		accepted, err := host.Apply(ctx, planID, key)
		return acceptMsg{requestID: id, accepted: accepted, err: err}
	}
}

func (m tuiModel) acceptance(msg acceptMsg) (tea.Model, tea.Cmd) {
	issued := m.issued
	if issued == nil || msg.requestID != issued.RequestID || !issued.InFlight {
		return m, nil
	}
	issued.InFlight = false
	if msg.err != nil {
		issued.Err = result.Classify(msg.err)
		return m, nil
	}
	issued.Accepted = &msg.accepted
	if m.screen != screenReview || m.reviewFlags != issued.Flags || m.review.Value.PlanID != issued.Review.PlanID {
		return m, nil
	}
	m.stopRead()
	m.screen = screenOperation
	return m.observeIssued()
}

func (m tuiModel) openIssued() (tea.Model, tea.Cmd) {
	if m.issued == nil {
		return m, nil
	}
	m.stopRead()
	m.confirm = false
	if m.issued.Accepted != nil {
		if m.screen != screenOperation {
			m = m.push(screenOperation)
		}
		return m.observeIssued()
	}
	m.review = loadable[tuiReview]{State: loadReady, Value: m.issued.Review, At: m.now()}
	m.reviewFlags, m.reviewOffset = m.issued.Flags, 0
	if m.screen != screenReview {
		m = m.push(screenReview)
	}
	return m, nil
}

func (m tuiModel) observeIssued() (tuiModel, tea.Cmd) {
	issued := m.issued
	id := issued.Accepted.OperationID
	if m.observe == nil || m.observe.Flags != issued.Flags || m.observe.Operation.ID != id {
		m.observe = &tuiObservation{Flags: issued.Flags, FromStart: true, Operation: ops.Operation{ID: id, App: issued.Review.App, PlanID: issued.Review.PlanID}}
	}
	return m.loadOperation()
}

func (m tuiModel) issuedPageError(status jobs.Status) error {
	issued := m.issued
	if issued == nil || issued.Accepted == nil || m.observe.Flags != issued.Flags || m.observe.Operation.ID != issued.Accepted.OperationID {
		return nil
	}
	if status.Operation.App != issued.Review.App || status.Operation.PlanID != issued.Review.PlanID {
		return result.New(result.TransportInvalidResponse, nil)
	}
	return nil
}

func (m *tuiModel) settleIssued() {
	if m.issued == nil || m.issued.Accepted == nil || m.observe.Flags != m.issued.Flags {
		return
	}
	op := m.observe.Operation
	if op.ID == m.issued.Accepted.OperationID && op.App == m.issued.Review.App && op.PlanID == m.issued.Review.PlanID {
		m.issued = nil
	}
}

func (m tuiModel) issuedExitNotice() string {
	issued := m.issued
	if issued == nil {
		return ""
	}
	if issued.Accepted == nil {
		return fmt.Sprintf("Acceptance of plan %s is unknown. Recover the same operation with: %s", issued.Review.PlanID, issued.Flags.command("apply", issued.Review.PlanID, "--idempotency-key", issued.Review.Key))
	}
	id := issued.Accepted.OperationID
	if m.observe == nil || m.observe.Flags != issued.Flags || m.observe.Operation.ID != id {
		return fmt.Sprintf("Operation %s is still running on %s; quitting did not stop it. Observe with: %s", id, issued.Flags.target, issued.Flags.command("status", "--operation", id))
	}
	if !m.observe.Operation.State.IsTerminal() {
		return m.observedExitNotice()
	}
	return fmt.Sprintf("Operation %s on %s has not been fully observed. Observe with: %s", id, issued.Flags.target, issued.Flags.command("status", "--operation", id, "--after-cursor", fmt.Sprint(m.observe.Cursor)))
}
