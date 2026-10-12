package host

import (
	"context"
	"errors"

	"github.com/ShaulLavo/brine/internal/apply"
	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/plan"
	"github.com/ShaulLavo/brine/internal/result"
)

func (s Service) managedPreparationPlan(ctx context.Context, facts apply.Facts) (plan.Plan, error) {
	input := facts.Input
	if input.Managed == nil || input.Managed.Runtime != nil {
		return plan.Plan{}, result.New(result.Conflict, nil)
	}
	preview, err := plan.Build(input)
	if err != nil {
		return plan.Plan{}, err
	}
	if preview.ManagedPreview == nil || preview.ManagedPreview.EngineHostPort == 0 || input.Desired.App().HasAppContainer() && preview.ManagedPreview.AppHostPort == 0 {
		return preview, nil
	}
	inspector, ok := s.Data.(preparationInspector)
	if !ok {
		return plan.Plan{}, result.New(result.DependencyMissing, nil)
	}
	roots, err := inspector.InspectPreparationRoots(ctx, input.Desired)
	if err != nil {
		return plan.Plan{}, err
	}
	if len(roots) != 1 {
		return plan.Plan{}, result.New(result.Conflict, nil)
	}
	proposed, err := managedProposal(input)
	if err != nil {
		return plan.Plan{}, err
	}
	proposed.EnginePort = preview.ManagedPreview.EngineHostPort
	if input.Desired.App().HasAppContainer() {
		proposed.Frontend = &plan.ManagedFrontendPlan{Image: input.Image, Port: preview.ManagedPreview.AppHostPort, Secrets: []plan.SecretBinding{}}
	}
	return plan.BuildManagedPreparation(input, proposed, roots[0])
}

func managedProposal(input plan.Input) (plan.ManagedServicePlan, error) {
	if input.Managed == nil || input.Desired.SpacetimeDB == nil {
		return plan.ManagedServicePlan{}, errors.New("managed proposal required")
	}
	proposed := plan.ManagedServicePlan{State: data.EngineUnpublished, EngineImage: input.Managed.EngineImage}
	if input.Managed.Database != nil {
		if input.Managed.Archive == nil {
			return proposed, errors.New("managed archive reservation missing")
		}
		proposed.Database = *input.Managed.Database
		proposed.Archive = *input.Managed.Archive
		return proposed, nil
	}
	ids := make([]string, 4)
	for i := range ids {
		id, err := data.NewID()
		if err != nil {
			return proposed, err
		}
		ids[i] = id
	}
	incarnation, database, binding, epoch := data.AppIncarnationID(ids[0]), data.DatabaseID(ids[1]), data.ReplicaBindingID(ids[2]), data.ReplicaEpochID(ids[3])
	relative, err := data.RelativeDirectory(incarnation, database)
	if err != nil {
		return proposed, err
	}
	destination := input.Desired.BackupDestinations[0]
	prefix, err := data.RemotePrefix(destination.BasePrefix, incarnation, database, epoch)
	if err != nil {
		return proposed, err
	}
	engine := input.Desired.SpacetimeDB
	proposed.Database = data.SpacetimeDBBinding{DatabaseID: database, Name: engine.Name, IncarnationID: incarnation, Root: engine.PersistentRoot, RelativeDirectory: relative, BindingID: binding}
	proposed.Archive = data.ArchiveBinding{BindingID: binding, DatabaseID: database, Destination: destination, EpochID: epoch, RemotePrefix: prefix}
	return proposed, nil
}
