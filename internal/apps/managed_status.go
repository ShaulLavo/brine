package apps

import (
	"context"
	"errors"
	"slices"

	"github.com/ShaulLavo/brine/internal/plan"
	"github.com/ShaulLavo/brine/internal/store"
	"github.com/ShaulLavo/brine/internal/target"
)

func (s Service) appendManagedReservations(ctx context.Context, state plan.BrineState, app string, report *Report) error {
	if s.Managed == nil {
		return nil
	}
	present := map[string]bool{}
	for _, status := range report.Apps {
		present[status.App] = true
	}
	for _, incarnation := range state.DataIncarnations {
		if present[incarnation.App] || app != "" && app != incarnation.App {
			continue
		}
		managed, err := s.Managed.Read(ctx, incarnation.App)
		if errors.Is(err, store.ErrNotFound) {
			continue
		}
		if err != nil {
			return err
		}
		if !managed.Valid() {
			return store.ErrInvalid
		}
		item := Status{App: incarnation.App, Managed: &managed, Health: Health{UnitActive: target.Observation[bool]{Status: target.Unknown}, Direct: "not_checked"}, Drift: Drift{State: "unknown", Fields: []string{}}}
		operation, err := s.Store.LastOperation(ctx, incarnation.App)
		if err == nil {
			item.LastOperation = &operation
		} else if !errors.Is(err, store.ErrNotFound) {
			return err
		}
		report.Apps = append(report.Apps, item)
	}
	return nil
}

func managedDrift(snapshot target.Snapshot, release plan.CurrentRelease) Drift {
	if release.Managed.Frontend != nil {
		frontend := release
		frontend.Units = nil
		for _, unit := range release.Units {
			if unit.Name == release.App+".container" {
				frontend.Units = append(frontend.Units, unit)
			}
		}
		drift := CompareDrift(snapshot, frontend)
		if drift.State != "drifted" {
			drift.State = "unknown"
		}
		return drift
	}
	drift := Drift{State: "unknown", Fields: []string{}}
	if snapshot.CaddyConfig.Status != target.KnownStatus || snapshot.CaddyConfig.Value == nil {
		return drift
	}
	if !slices.Contains(snapshot.CaddyConfig.Value.Files, release.CaddyFile) {
		drift.State = "drifted"
		drift.Fields = append(drift.Fields, "caddy")
	}
	return drift
}
