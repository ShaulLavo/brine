package cli

import (
	"errors"
	"strings"

	"github.com/ShaulLavo/brine/internal/apps"
	"github.com/ShaulLavo/brine/internal/plan"
	"github.com/ShaulLavo/brine/internal/result"
)

func tuiActionGoldenStates() map[string]tuiModel {
	review := func() tuiModel { return reviewedModel(fixtureHost()) }
	actions := review()
	actions.screen = screenActions
	ready := review()
	loading := review()
	loading.review.start()
	failed := review()
	failed.review.finish(tuiReview{}, result.New(result.TransportFailure, errors.New("password=hunter2")), fixtureReadTime)
	conflict := review()
	conflict.review.Value.Kind = plan.Conflict
	conflict.review.Value.Config.Kind = plan.Conflict
	conflict.review.Value.Config.Conflicts = []plan.Diagnostic{{Code: plan.ArtifactDrift, Field: "unit\x1b[31m"}}
	noop := review()
	noop.review.Value.Kind = plan.NoOp
	noop.review.Value.Config.Kind = plan.NoOp
	confirm := review()
	confirm.confirm = true
	rollback := review()
	p := apps.RollbackPlan{PlanID: lifecyclePlanID, Kind: plan.Update, ReleaseID: "rel-3c02aa10", Compatibility: "stateless_compatible", Conflicts: []plan.Diagnostic{}, Diff: &plan.ConfigurationDiff{Environment: &plan.EnvironmentChange{Changed: []string{"FIXTURE_MODE"}}}}
	rollback.review.Value = tuiReview{Action: actionRollback, App: "fixture-api", PlanID: p.PlanID, Kind: p.Kind, Rollback: &p}
	pending, _ := issueForTest(review())
	uncertain, _ := issueForTest(review())
	uncertain.issued.InFlight = false
	uncertain.issued.Err = result.New(result.TransportInvalidResponse, nil)
	long := review()
	long.review.Value.App = strings.Repeat("long-", 12) + "app"
	long.reviewFlags.target = strings.Repeat("target-", 8) + "name"
	long.confirm = true
	return map[string]tuiModel{"actions-ready": actions, "review-ready": ready, "review-loading": loading, "review-failed": failed, "review-conflict": conflict, "review-noop": noop, "review-confirm": confirm, "review-rollback": rollback, "review-accepting": pending, "review-uncertain": uncertain, "review-long-confirm": long}
}
