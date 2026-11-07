package cli

import (
	"context"

	"github.com/ShaulLavo/brine/internal/dispatch"
	"github.com/ShaulLavo/brine/internal/result"
	"github.com/ShaulLavo/brine/internal/spec"
)

type tuiDeploymentInput struct {
	App     spec.App
	Request dispatch.PlanArgs
}

const tuiDeploymentScopeGuidance = "TUI release review supports stateless HTTP specs only. Use brine deploy for databases, managed engines or UDP."

func readTUIDeploymentInput(ctx context.Context, path string) (tuiDeploymentInput, error) {
	if err := ctx.Err(); err != nil {
		return tuiDeploymentInput{}, err
	}
	raw, err := readOfflineInput(path, dispatch.RequestLimit/2)
	if err != nil {
		return tuiDeploymentInput{}, err
	}
	app, err := spec.Parse(raw)
	if err != nil {
		return tuiDeploymentInput{}, result.New(result.InvalidUsage, err)
	}
	if len(app.Databases) != 0 || app.SpacetimeDB != nil || app.UDP != nil {
		return tuiDeploymentInput{}, result.New(result.InvalidUsage, nil)
	}
	request, err := dispatch.NewPlanArgs(string(raw), app)
	if err != nil {
		return tuiDeploymentInput{}, result.New(result.InvalidUsage, err)
	}
	return tuiDeploymentInput{App: app, Request: request}, nil
}
