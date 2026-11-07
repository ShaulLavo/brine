package cli

import (
	"strings"

	"github.com/ShaulLavo/brine/internal/dispatch"
	"github.com/ShaulLavo/brine/internal/jobs"
	"github.com/ShaulLavo/brine/internal/ops"
	"github.com/ShaulLavo/brine/internal/plan"
	"github.com/ShaulLavo/brine/internal/result"
	"github.com/ShaulLavo/brine/internal/spec"
)

func deploymentGoldenModel(kind plan.Kind) tuiModel {
	m := fixtureModel(fixtureHost(), "")
	m.stopRead()
	p := dispatch.Planned{PlanID: lifecyclePlanID, Kind: kind}
	if kind == plan.Conflict {
		p.Conflicts = []plan.Diagnostic{{Code: plan.ArtifactDrift, Field: "unit"}}
	}
	summary := deploymentReviewSummary{Image: "ghcr.io/example/hello@" + fixtureDigest("a"), ContainerPort: 3000, Domains: []string{"hello.example.com"}, Health: spec.Health{Path: "/", ExpectedStatus: 200, StartupDeadlineSeconds: 30, TimeoutSeconds: 3}, EnvironmentKeys: []string{"A_KEY", "Z_KEY"}}
	m.entry = tuiEntry{Spec: "brine.toml"}
	m.screen, m.reviewFlags = screenReview, operationFlags{target: "fixture"}
	m.review = loadable[tuiReview]{State: loadReady, At: fixtureReadTime, Value: tuiReview{Action: actionDeploy, App: "hello", PlanID: p.PlanID, Kind: kind, Deployment: &p, DeploymentSummary: &summary}}
	return m
}

func tuiDeploymentGoldenStates() map[string]tuiModel {
	ready := deploymentGoldenModel(plan.Create)
	update := deploymentGoldenModel(plan.Update)
	loading := deploymentGoldenModel(plan.Create)
	loading.review.start()
	failed := deploymentGoldenModel(plan.Create)
	failed.review.State, failed.review.Err = loadFailed, result.New(result.InvalidUsage, nil)
	confirm := deploymentGoldenModel(plan.Create)
	confirm.confirm = true
	uncertain := deploymentGoldenModel(plan.Update)
	uncertain.review.Value.Key = "release-original-key"
	uncertain.issued = &issuedApply{Flags: uncertain.reviewFlags, Review: uncertain.review.Value, Err: result.New(result.TransportInvalidResponse, nil)}
	long := deploymentGoldenModel(plan.Create)
	long.review.Value.App = strings.Repeat("long-", 12) + "app"
	long.reviewFlags.target = strings.Repeat("target-", 8) + "name"
	long.review.Value.DeploymentSummary.Domains = []string{strings.Repeat("long", 16) + ".example.com", "unicode-界.example.test\x1b]52;c;secret\a"}
	long.review.Value.DeploymentSummary.Health.Path = "/health界\x1b[31m"
	long.confirm = true
	direct := deploymentGoldenModel(plan.Create)
	direct.entry, direct.screen = tuiEntry{Operation: "op-direct"}, screenOperation
	page := operationFixture(ops.Failed, 0, jobs.EventPageLimit)
	page.Operation.ID = "op-direct"
	direct.observe = &tuiObservation{Flags: direct.reviewFlags, Bound: true, Operation: page.Operation, Events: page.Events, Cursor: page.NextCursor, At: fixtureReadTime, FromStart: true, Phase: observePaused, Err: result.New(result.TransportFailure, nil)}
	return map[string]tuiModel{
		"deployment-create": ready, "deployment-update-unchanged": update, "deployment-loading": loading, "deployment-invalid": failed,
		"deployment-conflict": deploymentGoldenModel(plan.Conflict), "deployment-noop": deploymentGoldenModel(plan.NoOp), "deployment-confirm": confirm,
		"deployment-uncertain": uncertain, "deployment-long-controls": long, "operation-direct-undrained": direct,
	}
}
