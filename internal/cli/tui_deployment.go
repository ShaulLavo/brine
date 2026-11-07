package cli

import (
	"context"
	"slices"

	tea "charm.land/bubbletea/v2"
	"github.com/ShaulLavo/brine/internal/ops"
	"github.com/ShaulLavo/brine/internal/result"
	"github.com/ShaulLavo/brine/internal/spec"
)

type tuiEntry struct {
	Spec      string
	Operation string
}

type deploymentReviewSummary struct {
	Image           string
	ContainerPort   int
	Domains         []string
	Health          spec.Health
	EnvironmentKeys []string
}

func (h connectedHost) DeploymentPlan(ctx context.Context, sourcePath string) (tuiReview, error) {
	input, err := readTUIDeploymentInput(ctx, sourcePath)
	if err != nil {
		return tuiReview{Action: actionDeploy}, err
	}
	summary := summarizeDeployment(input.App)
	p, err := h.flags.deploymentPlan(ctx, h.deps, "plan", input.Request)
	if err != nil {
		return tuiReview{Action: actionDeploy, App: string(input.App.Name)}, err
	}
	if p.Managed != nil || len(p.SchemaArtifacts) != 0 {
		return tuiReview{Action: actionDeploy, App: string(input.App.Name)}, result.New(result.TransportInvalidResponse, nil)
	}
	return tuiReview{Action: actionDeploy, App: string(input.App.Name), PlanID: p.PlanID, Kind: p.Kind, Deployment: &p, DeploymentSummary: &summary}, nil
}

func summarizeDeployment(app spec.App) deploymentReviewSummary {
	summary := deploymentReviewSummary{Image: string(app.Image), ContainerPort: int(app.ContainerPort), Health: app.Health, Domains: make([]string, len(app.Domains)), EnvironmentKeys: make([]string, 0, len(app.Environment))}
	for i, domain := range app.Domains {
		summary.Domains[i] = string(domain)
	}
	for key := range app.Environment {
		summary.EnvironmentKeys = append(summary.EnvironmentKeys, key)
	}
	slices.Sort(summary.EnvironmentKeys)
	return summary
}

func (m tuiModel) loadEntry() (tuiModel, tea.Cmd) {
	if m.entry.Operation != "" {
		if m.observe != nil && m.observe.Flags != m.flags {
			m.screen = screenApps
			return m.loadApps()
		}
		m.screen = screenOperation
		if m.observe == nil || m.observe.Flags != m.flags || m.observe.Operation.ID != m.entry.Operation {
			m.observe = &tuiObservation{Flags: m.flags, FromStart: true, Operation: ops.Operation{ID: m.entry.Operation}}
		}
		return m.loadOperation()
	}
	if m.entry.Spec != "" {
		m.screen = screenReview
		if m.issued != nil {
			m.screen = screenApps
			return m.loadApps()
		}
		m.review = loadable[tuiReview]{Value: tuiReview{Action: actionDeploy}}
		m.reviewFlags, m.reviewOffset = m.flags, 0
		return m.loadReview()
	}
	m.screen = screenApps
	return m.loadApps()
}
