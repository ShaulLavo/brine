package cli

import (
	"fmt"
	"strings"

	"github.com/ShaulLavo/brine/internal/apps"
	"github.com/ShaulLavo/brine/internal/plan"
	"github.com/ShaulLavo/brine/internal/planview"
	"github.com/ShaulLavo/brine/internal/ui"
)

func (m tuiModel) actionsScreen() ui.Screen {
	rows := make([][]ui.Cell, 0, 4)
	for _, action := range m.actions() {
		rows = append(rows, []ui.Cell{{Text: actionLabel(action)}, {Text: actionDescription(action)}})
	}
	return ui.Screen{Title: "brine · " + m.flags.target + " › " + m.app + " › plan", Body: m.message("Choose a change to plan. Nothing changes until you review and apply it.", ""), Help: m.help(helpKey{"enter plan", 0}, keyBack), Table: &ui.Table{Columns: []ui.Column{{Title: "ACTION", Min: 10, Max: 32, Priority: 2}, {Title: "EFFECT", Min: 15, Max: 70, Priority: 1}}, Rows: rows, Selected: m.actionIndex}}
}

func actionLabel(action tuiAction) string {
	if action == actionRollback {
		return "Roll back to previous known-good"
	}
	return strings.ToUpper(string(action[:1])) + string(action[1:])
}

func actionDescription(action tuiAction) string {
	switch action {
	case actionRestart:
		return "restart app; releases and data unchanged"
	case actionStop:
		return "stop app; releases and data unchanged"
	case actionStart:
		return "start the app unit"
	default:
		return "previous known-good release; never rewinds data"
	}
}

func (m tuiModel) reviewScreen() ui.Screen {
	r := m.review.Value
	back := helpKey{"esc discard", 0}
	if m.width < tuiNarrow {
		back = keyBack
	}
	s := ui.Screen{Title: "brine · " + m.reviewFlags.target + " › " + r.App + " › review", Context: string(r.Kind), Help: m.help(helpKey{"↑↓ scroll", 0}, back)}
	s = withError(s, m.review.Err)
	s.Body = m.reviewLines()
	if m.canApply() {
		s.Help = append([]string{"a apply"}, s.Help...)
	}
	if m.issued != nil {
		s.Notice, s.NoticeTone = m.issuedNotice()
	}
	if m.confirm {
		s.Notice = fmt.Sprintf("Confirm: apply %s (%s) to %s on %s? y apply · any other key cancels", shortDigest(r.PlanID), r.Action, r.App, m.reviewFlags.target)
		s.NoticeTone = ui.Warn
		if !m.frame().NoticeVisible(s) {
			s.Help = m.help(helpKey{"resize to confirm", 0}, helpKey{"esc cancel", 0})
		}
	}
	s.Body = topWindow(s.Body, m.reviewOffset, m.frame().BodyRows(s))
	return s
}

func (m tuiModel) reviewLines() []string {
	r := m.review.Value
	if m.review.State == loadLoading {
		return m.message(fmt.Sprintf("Planning %s for %s on %s…", r.Action, r.App, m.reviewFlags.target))
	}
	if m.review.State == loadFailed {
		if r.Action == actionDeploy {
			return m.message("Could not plan the local spec. Refresh to plan again.", tuiDeploymentScopeGuidance)
		}
		return m.message("Could not read the host plan. Refresh to plan again.")
	}
	lines := m.message(actionLabel(r.Action)+" "+r.App, "plan "+r.PlanID)
	if r.Deployment != nil && r.DeploymentSummary != nil {
		lines = append(lines, m.message(deploymentSummaryLines(*r.DeploymentSummary)...)...)
		lines = append(lines, planview.DiffLines(r.Deployment.Diff)...)
		lines = append(lines, conflictLines(r.Deployment.Conflicts)...)
	}
	if r.Config != nil {
		lines = append(lines, lifecycleReviewLines(*r.Config)...)
	}
	if r.Rollback != nil {
		p := r.Rollback
		lines = append(lines, m.message("Release "+p.ReleaseID, "Compatibility "+p.Compatibility+". App rollback never rewinds data.")...)
		lines = append(lines, planview.DiffLines(p.Diff)...)
		lines = append(lines, conflictLines(p.Conflicts)...)
	}
	if !r.applyable() {
		lines = append(lines, m.message("Apply disabled. No changes or unresolved plan conflicts. Plan again after resolving conflicts.")...)
	}
	if r.Key != "" {
		lines = append(lines, m.message("Idempotency key "+r.Key+" (retained with the original target/config)")...)
	}
	return lines
}

func lifecycleReviewLines(p apps.ConfigPlan) []string {
	lines := []string{fmt.Sprintf("Action: %s. Releases and app data stay unchanged.", p.Lifecycle)}
	if p.ManagedLifecycle != nil {
		v := p.ManagedLifecycle
		lines = append(lines, fmt.Sprintf("Managed components: engine=%t frontend=%t ingress=%t.", v.Engine, v.Frontend, v.Ingress))
	}
	lines = append(lines, planview.DiffLines(p.Diff)...)
	return append(lines, conflictLines(p.Conflicts)...)
}

func conflictLines(conflicts []plan.Diagnostic) []string {
	lines := make([]string, 0, len(conflicts))
	for _, conflict := range conflicts {
		lines = append(lines, planview.ConflictText(conflict))
	}
	return lines
}

func (m tuiModel) issuedNotice() (string, ui.Tone) {
	i := m.issued
	if i.Accepted != nil {
		return "Operation " + i.Accepted.OperationID + " on " + i.Flags.target + "; u observes the original action.", ui.Warn
	}
	if i.InFlight {
		return "Accepting with idempotency key " + i.Review.Key + ". Browsing does not cancel it.", ui.Warn
	}
	return errorNotice(i.Err) + " Acceptance is unknown; u reviews same-key recovery on " + i.Flags.target + ".", ui.Bad
}

func (m tuiModel) clampReviewOffset(offset int) int {
	if m.screen != screenReview {
		return 0
	}
	rows := m.frame().BodyRows(m.reviewScreen())
	return max(0, min(offset, len(m.reviewLines())-rows))
}
