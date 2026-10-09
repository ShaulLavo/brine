package apps

import (
	"context"
	"errors"
	"slices"
	"strconv"
	"strings"

	"github.com/ShaulLavo/brine/internal/plan"
	"github.com/ShaulLavo/brine/internal/policy"
	"github.com/ShaulLavo/brine/internal/result"
	"github.com/ShaulLavo/brine/internal/spec"
	"github.com/ShaulLavo/brine/internal/store"
	"github.com/ShaulLavo/brine/internal/target"
)

// Edit is a boundary value. Empty Action means set; unset, add and remove are
// restricted to environment and domains respectively. Values are private input.
type Edit struct {
	Key    string `json:"key"`
	Value  string `json:"value"`
	Action string `json:"action"`
}
type ConfigPlan struct {
	Lifecycle plan.ChangeKind         `json:"lifecycle,omitempty"`
	PlanID    string                  `json:"plan_id"`
	Kind      plan.Kind               `json:"kind"`
	Diff      *plan.ConfigurationDiff `json:"diff"`
	Conflicts []plan.Diagnostic       `json:"conflicts"`
}

func SpecFromDesired(d policy.Desired) spec.App {
	a := spec.App{SchemaVersion: d.SchemaVersion, Name: d.Name, Image: d.Image, ContainerPort: d.ContainerPort, Domains: slices.Clone(d.Domains), Health: spec.Health{Path: d.Health.Path, ExpectedStatus: d.Health.ExpectedStatus, StartupDeadlineSeconds: d.Health.StartupDeadlineSeconds, TimeoutSeconds: d.Health.TimeoutSeconds}, Resources: &spec.Resources{MemoryMB: d.Resources.MemoryMB, PIDsLimit: d.Resources.PIDsLimit}, Environment: map[string]string{}, Secrets: map[string]spec.SecretReference{}}
	for _, v := range d.Environment {
		a.Environment[v.Name] = v.Value
	}
	for _, v := range d.Secrets {
		a.Secrets[v.Name] = v.Reference
	}
	return a
}

func applyEdits(d policy.Desired, edits []Edit, p policy.Policy) (policy.Desired, error) {
	bad := func() (policy.Desired, error) { return policy.Desired{}, result.New(result.InvalidUsage, nil) }
	if len(edits) == 0 || len(edits) > 128 {
		return bad()
	}
	a := SpecFromDesired(d)
	for _, edit := range edits {
		if edit.Action != "" && edit.Action != "set" && edit.Action != "unset" && edit.Action != "add" && edit.Action != "remove" {
			return bad()
		}
		if len(edit.Key) > 256 || len(edit.Value) > 32<<10 {
			return bad()
		}
		if name, ok := strings.CutPrefix(edit.Key, "environment."); ok {
			if !spec.ValidEnvironmentName(name) || edit.Action == "add" || edit.Action == "remove" {
				return bad()
			}
			if edit.Action == "unset" {
				if edit.Value != "" {
					return bad()
				}
				delete(a.Environment, name)
			} else {
				a.Environment[name] = edit.Value
			}
			continue
		}
		if edit.Key == "domains" {
			switch edit.Action {
			case "add":
				a.Domains = append(a.Domains, spec.Domain(edit.Value))
			case "remove":
				domain, err := target.CanonicalDomain(edit.Value)
				if err != nil {
					return bad()
				}
				a.Domains = slices.DeleteFunc(a.Domains, func(d spec.Domain) bool { return string(d) == domain })
			default:
				return bad()
			}
			continue
		}
		if edit.Action != "" && edit.Action != "set" {
			return bad()
		}
		if name, ok := strings.CutPrefix(edit.Key, "secrets."); ok {
			if name == "" {
				return bad()
			}
			a.Secrets[name] = spec.SecretReference(edit.Value)
			continue
		}
		if edit.Key == "health.path" {
			a.Health.Path = spec.HealthPath(edit.Value)
			continue
		}
		n, err := strconv.Atoi(edit.Value)
		if err != nil {
			return bad()
		}
		switch edit.Key {
		case "resources.memory_mb":
			a.Resources.MemoryMB = n
		case "resources.pids_limit":
			a.Resources.PIDsLimit = n
		case "health.expected_status":
			a.Health.ExpectedStatus = n
		case "health.startup_deadline_seconds":
			a.Health.StartupDeadlineSeconds = n
		case "health.timeout_seconds":
			a.Health.TimeoutSeconds = n
		default:
			return bad()
		}
	}
	return policy.Normalize(a, p)
}

func (s Service) currentInput(ctx context.Context, app string) (plan.Input, policy.Policy, error) {
	if !appPattern.MatchString(app) || s.LoadPolicy == nil {
		return plan.Input{}, policy.Policy{}, result.New(result.DependencyMissing, nil)
	}
	pol, err := s.LoadPolicy(ctx)
	if err != nil {
		return plan.Input{}, pol, err
	}
	snap, state, err := s.observed(ctx)
	if err != nil {
		return plan.Input{}, pol, err
	}
	for _, r := range state.Releases {
		if r.App != app {
			continue
		}
		release, err := s.Store.ReleaseByID(ctx, app, r.ID)
		if err != nil {
			return plan.Input{}, pol, err
		}
		stored, d, err := s.Store.LoadPlan(ctx, release.PlanID)
		if errors.Is(err, store.ErrNotFound) {
			return plan.Input{}, pol, result.New(result.Conflict, nil)
		}
		if err != nil {
			return plan.Input{}, pol, err
		}
		if stored.App != app || stored.Target != snap.Identity || string(d.Name) != app {
			return plan.Input{}, pol, result.New(result.Conflict, nil)
		}
		return plan.Input{Desired: d, Snapshot: snap, State: state, Image: r.Image}, pol, nil
	}
	return plan.Input{}, pol, result.New(result.AppNotFound, nil)
}
func (s Service) saveConfig(ctx context.Context, p plan.Plan, d policy.Desired) (ConfigPlan, error) {
	id, err := s.Store.SavePlan(ctx, p, d)
	if err != nil {
		return ConfigPlan{}, err
	}
	return ConfigPlan{Lifecycle: p.Lifecycle, PlanID: id, Kind: p.Kind, Diff: p.Diff, Conflicts: p.Conflicts}, nil
}
func (s Service) ConfigSet(ctx context.Context, app string, edits []Edit) (ConfigPlan, error) {
	in, pol, err := s.currentInput(ctx, app)
	if err != nil {
		return ConfigPlan{}, err
	}
	in.Desired, err = applyEdits(in.Desired, edits, pol)
	if err != nil {
		return ConfigPlan{}, err
	}
	p, err := plan.Build(in)
	if err != nil {
		return ConfigPlan{}, err
	}
	return s.saveConfig(ctx, p, in.Desired)
}
func (s Service) Lifecycle(ctx context.Context, app string, action plan.ChangeKind) (ConfigPlan, error) {
	in, pol, err := s.currentInput(ctx, app)
	if err != nil {
		return ConfigPlan{}, err
	}
	in.Desired, err = policy.Normalize(SpecFromDesired(in.Desired), pol)
	if err != nil {
		return ConfigPlan{}, err
	}
	p, err := plan.BuildLifecycle(in, action)
	if err != nil {
		return ConfigPlan{}, err
	}
	return s.saveConfig(ctx, p, in.Desired)
}
