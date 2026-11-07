//go:build linux

package host

import (
	"context"
	"encoding/json"
	"time"

	"github.com/ShaulLavo/brine/internal/apps"
	"github.com/ShaulLavo/brine/internal/archive"
	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/dispatch"
	"github.com/ShaulLavo/brine/internal/jobs"
	"github.com/ShaulLavo/brine/internal/ops"
	"github.com/ShaulLavo/brine/internal/plan"
	"github.com/ShaulLavo/brine/internal/result"
	"github.com/ShaulLavo/brine/internal/store"
	"github.com/ShaulLavo/brine/internal/target"
)

type sqliteRetireOperations struct {
	workflow archive.Service
	jobs     jobs.Service
	state    *store.Store
}

func (o sqliteRetireOperations) plan(ctx context.Context, app string) (apps.ConfigPlan, error) {
	p, err := o.workflow.Plan(ctx, app)
	if err != nil {
		return apps.ConfigPlan{}, err
	}
	summary := archive.RemovalSummary{ArchiveID: p.ArchiveID, ExpiresAt: p.Removal.ExpiresAt, ReleaseID: p.Candidate.ReleaseID, RetainDays: 30, Units: []target.Unit{}, Databases: []archive.RemovalDatabaseSummary{}}
	if p.Candidate.ReleaseID != "" {
		release, err := o.state.ReleaseByID(ctx, app, p.Candidate.ReleaseID)
		if err != nil {
			return apps.ConfigPlan{}, err
		}
		summary.Route, summary.Units = release.CaddyFile, release.Units
	}
	for _, variant := range p.Candidate.Databases {
		d, _ := variant.SQLite()
		item := archive.RemovalDatabaseSummary{Name: d.Binding.Name, DatabaseID: d.Binding.DatabaseID, Arm: "never_written"}
		bindRemovalSummary(p, d, &item)
		summary.Databases = append(summary.Databases, item)
	}
	if err = o.summaryArms(ctx, p, &summary); err != nil {
		return apps.ConfigPlan{}, err
	}
	out := apps.ConfigPlan{Archive: &summary, Lifecycle: plan.RemoveApp, PlanID: p.ID, Kind: plan.Update, Conflicts: []plan.Diagnostic{}}
	out.SecretRetention = "d5_retained_releases"
	return out, nil
}

func (o sqliteRetireOperations) summaryArms(ctx context.Context, p archive.Plan, summary *archive.RemovalSummary) error {
	for i := range summary.Databases {
		item := &summary.Databases[i]
		if item.PreparationID == "" {
			continue
		}
		source, err := o.state.ReadSQLitePreparedSource(ctx, p.Candidate.App, string(item.Name), item.PreparationID)
		if err != nil {
			return err
		}
		if source.Point.Kind == data.RestorePointLTX {
			item.Arm = "prepared_ltx"
		}
	}
	return nil
}

// Apply resolves the archive namespace first. Lookup corruption never becomes a deployment.
func (o sqliteRetireOperations) Apply(ctx context.Context, id, key string) (jobs.Accepted, error) {
	if !jobs.ValidPlanID(id) || !jobs.ValidID(key) {
		return jobs.Accepted{}, result.New(result.InvalidUsage, nil)
	}
	p, found, err := o.state.LookupRemovalPlan(ctx, id)
	if err != nil {
		return jobs.Accepted{}, err
	}
	if !found && p.ID != "" {
		return jobs.Accepted{}, result.New(result.PolicyRefused, nil)
	}
	if !found {
		return o.jobs.Apply(ctx, id, key)
	}
	if p.Requester != o.jobs.Requester {
		return jobs.Accepted{}, result.New(result.PolicyRefused, nil)
	}
	if !time.Now().UTC().Before(p.Removal.ExpiresAt) {
		return jobs.Accepted{}, result.New(result.PlanStale, nil)
	}
	return o.submit(ctx, store.SQLiteRetireInput{Kind: ops.SQLiteRetire, App: p.Candidate.App, PlanID: p.ID}, key)
}
func (o sqliteRetireOperations) Operation(ctx context.Context, id string, cursor uint64) (jobs.Status, error) {
	return o.jobs.Operation(ctx, id, cursor)
}

func (o sqliteRetireOperations) submit(ctx context.Context, in store.SQLiteRetireInput, key string) (jobs.Accepted, error) {
	id, err := o.state.SaveSQLiteRetireInput(ctx, in)
	if err != nil {
		return jobs.Accepted{}, err
	}
	return o.jobs.Submit(ctx, ops.Intent{Kind: in.Kind, App: in.App, SecretRef: id, RecoveryOf: in.OriginalOperationID}, key)
}

func (o sqliteRetireOperations) InspectSQLiteArchive(ctx context.Context, a dispatch.SQLiteRetireInspectArgs) (archive.Inspection, error) {
	p, err := o.state.LoadArchivePlanByArchive(ctx, a.ArchiveID)
	if err != nil || p.Removal == nil || p.Requester != o.jobs.Requester {
		return archive.Inspection{}, result.New(result.PolicyRefused, err)
	}
	return o.workflow.Inspect(ctx, a.ArchiveID)
}
func (o sqliteRetireOperations) ResolveSQLiteArchive(ctx context.Context, a dispatch.SQLiteRetireResolveArgs) (jobs.Accepted, error) {
	original, err := o.state.GetOperation(ctx, a.OperationID)
	if err != nil || original.Kind != ops.SQLiteRetire || original.State != ops.RecoveryRequired || original.Requester != o.jobs.Requester {
		return jobs.Accepted{}, result.New(result.PolicyRefused, err)
	}
	in, err := o.state.ReadSQLiteRetireInput(ctx, original.ID)
	if err != nil {
		return jobs.Accepted{}, err
	}
	p, found, err := o.state.LookupRemovalPlan(ctx, in.PlanID)
	if err != nil || !found {
		return jobs.Accepted{}, result.New(result.PolicyRefused, err)
	}
	if err = checkSQLiteRemovalResolution(ctx, o.workflow, p, a.Disposition); err != nil {
		return jobs.Accepted{}, err
	}
	return o.submit(ctx, store.SQLiteRetireInput{Kind: ops.SQLiteRetireResolve, App: original.App, PlanID: in.PlanID, OriginalOperationID: original.ID, Disposition: a.Disposition}, a.IdempotencyKey)
}

func (o sqliteRetireOperations) task(ctx context.Context, op ops.Operation) (json.RawMessage, error) {
	in, err := o.state.ReadSQLiteRetireInput(ctx, op.ID)
	if err != nil || in.Kind != op.Kind || in.App != op.App || in.OriginalOperationID != op.RecoveryOf {
		return nil, result.New(result.PolicyRefused, err)
	}
	p, found, err := o.state.LookupRemovalPlan(ctx, in.PlanID)
	if err != nil || !found || p.Requester != op.Requester || p.Candidate.App != op.App {
		return nil, result.New(result.PolicyRefused, err)
	}
	workflow := o.workflow
	workflow.Operation, workflow.Requester = op.ID, op.Requester
	if op.Kind == ops.SQLiteRetire {
		_, err = workflow.Prepare(ctx, op.App, p.ID)
		if err == nil {
			_, err = workflow.Retire(ctx, p.ArchiveID)
		}
	} else if op.Kind == ops.SQLiteRetireResolve {
		err = resolveSQLiteRemoval(ctx, workflow, p, in.Disposition)
	} else {
		return nil, result.New(result.PolicyRefused, nil)
	}
	if err != nil {
		return nil, err
	}
	receipt, found, err := o.state.ReadSQLiteRetireSuccess(ctx, op)
	if err != nil || !found {
		return nil, result.New(result.RecoveryRequired, err)
	}
	return archive.EncodeRemovalReceipt(receipt)
}

func resolveSQLiteRemoval(ctx context.Context, workflow archive.Service, p archive.Plan, disposition string) error {
	switch disposition {
	case "continue":
		inspection, err := workflow.Inspect(ctx, p.ArchiveID)
		if err != nil {
			return err
		}
		if !inspection.CanReconcile {
			return result.New(result.ContinueUnavailable, nil)
		}
		_, err = workflow.Reconcile(ctx, p.ArchiveID)
		return err
	case "abandon":
		_, err := workflow.Abandon(ctx, p.ArchiveID)
		return err
	default:
		return result.New(result.InvalidUsage, nil)
	}
}

func wireSQLiteRemoval(r *Runtime, app apps.Service, workflow archive.Service, tasks jobs.Service, state *store.Store) {
	adapter := workflow.Host.(archiveHost)
	adapter.removal = true
	workflow.Host = adapter
	removal := sqliteRetireOperations{workflow: workflow, jobs: tasks, state: state}
	app.PersistentRemoval = removal.plan
	r.Config, r.Apps, r.Jobs, r.SQLiteRetire = app, app, removal, removal
	r.Runner.TaskHandlers[ops.SQLiteRetire], r.Runner.TaskHandlers[ops.SQLiteRetireResolve] = removal.task, removal.task
}

func bindRemovalSummary(p archive.Plan, d archive.SQLiteDatabase, item *archive.RemovalDatabaseSummary) {
	if !d.Written {
		return
	}
	for _, ref := range p.Removal.Points {
		if ref.DatabaseID != item.DatabaseID {
			continue
		}
		item.PreparationID, item.SourcePointID, item.Cutoff = ref.PreparationID, ref.SourcePointID, ref.Cutoff
		item.Arm = "prepared_snapshot"
	}
}

func checkSQLiteRemovalResolution(ctx context.Context, workflow archive.Service, p archive.Plan, disposition string) error {
	i, err := workflow.Inspect(ctx, p.ArchiveID)
	if err != nil {
		return err
	}
	switch disposition {
	case "continue":
		if !i.CanReconcile {
			return result.New(result.ContinueUnavailable, nil)
		}
	case "abandon":
		if i.Preparation == nil || !i.Preparation.CanAbandon {
			return result.New(result.RecoveryRequired, nil)
		}
	default:
		return result.New(result.InvalidUsage, nil)
	}
	return nil
}
