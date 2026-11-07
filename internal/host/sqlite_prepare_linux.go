//go:build linux

package host

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"time"

	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/dispatch"
	"github.com/ShaulLavo/brine/internal/jobs"
	"github.com/ShaulLavo/brine/internal/ops"
	"github.com/ShaulLavo/brine/internal/result"
	"github.com/ShaulLavo/brine/internal/sqliteprep"
	"github.com/ShaulLavo/brine/internal/systemd"
)

// sqliteSettlementMargin leaves the runner time to settle before the detached
// job's runtime limit stops it.
const sqliteSettlementMargin = time.Minute

// sqlitePreparations admits SQLite prepare/abandon tasks and runs them through
// the one sqliteprep engine with the protected store journal and real host.
type sqlitePreparations struct {
	service Service
	jobs    jobs.Service
	host    sqlitePreparationHost
}

// sqlitePreparationID is derived from the requester, the selected database,
// the predecessor and the idempotency key, so a retried admission names the
// same canonical input and therefore the same operation.
func sqlitePreparationID(requester string, database data.DatabaseID, supersedes, key string) string {
	sum := sha256.Sum256([]byte("brine-sqlite-preparation-v1\x00" + requester + "\x00" + string(database) + "\x00" + supersedes + "\x00" + key))
	return hex.EncodeToString(sum[:16])
}

func (o sqlitePreparations) Prepare(ctx context.Context, args dispatch.SQLitePrepareArgs) (jobs.Accepted, error) {
	if !args.Valid() {
		return jobs.Accepted{}, result.New(result.InvalidUsage, nil)
	}
	if err := o.service.Authorize(ctx, dispatch.Mutating); err != nil {
		return jobs.Accepted{}, err
	}
	scopes, err := o.service.Store.ReadCredentialScopes(ctx, args.App)
	if err != nil {
		return jobs.Accepted{}, result.New(result.PolicyRefused, nil)
	}
	scope, err := selectCredentialScope(scopes, args.Database)
	if err != nil {
		return jobs.Accepted{}, result.New(result.PolicyRefused, nil)
	}
	database := scope.Database.DatabaseID
	if args.Supersedes != "" {
		owner, err := o.service.Store.SQLitePreparationOwner(ctx, args.Supersedes)
		if err != nil || owner.App != args.App || owner.Database != database || owner.Requester != o.jobs.Requester {
			return jobs.Accepted{}, result.New(result.PolicyRefused, nil)
		}
	}
	input := sqliteprep.Input{Kind: sqliteprep.PrepareTask, PreparationID: sqlitePreparationID(o.jobs.Requester, database, args.Supersedes, args.IdempotencyKey), App: args.App, DatabaseID: database, Supersedes: args.Supersedes, DeclarationPlanID: args.DeclarationPlanID, DeliveredCredentialVersion: args.DeliveredCredentialVersion}
	return o.submit(ctx, input, ops.SQLitePrepare, args.IdempotencyKey)
}

func (o sqlitePreparations) Abandon(ctx context.Context, args dispatch.SQLiteAbandonArgs) (jobs.Accepted, error) {
	if !args.Valid() {
		return jobs.Accepted{}, result.New(result.InvalidUsage, nil)
	}
	if err := o.service.Authorize(ctx, dispatch.Mutating); err != nil {
		return jobs.Accepted{}, err
	}
	owner, err := o.service.Store.SQLitePreparationOwner(ctx, args.PreparationID)
	if err != nil || owner.Requester != o.jobs.Requester {
		return jobs.Accepted{}, result.New(result.PolicyRefused, nil)
	}
	input := sqliteprep.Input{Kind: sqliteprep.AbandonTask, PreparationID: args.PreparationID, App: owner.App, DatabaseID: owner.Database}
	return o.submit(ctx, input, ops.SQLitePrepareAbandon, args.IdempotencyKey)
}

func (o sqlitePreparations) submit(ctx context.Context, input sqliteprep.Input, kind ops.Kind, key string) (jobs.Accepted, error) {
	reference, err := o.service.Store.SaveSQLiteTaskInput(ctx, input)
	if err != nil {
		return jobs.Accepted{}, result.New(result.PolicyRefused, err)
	}
	return o.jobs.Submit(ctx, ops.Intent{Kind: kind, App: input.App, SecretRef: reference}, key)
}

// task runs one detached SQLite preparation task. Its effective budget is the
// detached job's runtime limit less a settlement margin, never longer than
// the twenty-minute preparation budget. A refusal before the engine runs is
// typed, so the runner fails it only after the store confirms no claim.
func (o sqlitePreparations) task(kind sqliteprep.TaskKind) jobs.TaskHandler {
	return func(ctx context.Context, op ops.Operation) (json.RawMessage, error) {
		task, input, err := o.service.Store.SQLiteTask(ctx, op.ID)
		if err != nil || task.Kind != kind || task.Requester != op.Requester || task.App != op.App || task.InputSHA256 != op.SecretRef {
			return nil, errors.Join(&sqliteprep.Refusal{Code: sqliteprep.InvalidEvidence}, err)
		}
		service := o.service
		service.Requester = op.Requester
		if err = service.Authorize(ctx, dispatch.Mutating); err != nil {
			return nil, errors.Join(&sqliteprep.Refusal{Code: sqliteprep.InvalidEvidence}, err)
		}
		ctx, cancel := context.WithTimeout(ctx, systemd.JobRuntimeLimit-sqliteSettlementMargin)
		defer cancel()
		engine := sqliteprep.Service{Journal: o.service.Store.SQLiteObservedPreparationJournal(observeSQLiteLocation, observeSQLiteSchema), Host: o.host}
		run := engine.Prepare
		if kind == sqliteprep.AbandonTask {
			run = engine.Abandon
		}
		// A cleanup error after known atomic success is diagnostic only; the
		// committed receipt stays the outcome.
		r, err := run(ctx, task, input)
		if err != nil {
			return nil, err
		}
		return sqliteprep.Encode(r.Receipt)
	}
}
